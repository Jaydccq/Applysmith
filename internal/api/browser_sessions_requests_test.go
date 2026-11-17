package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/browser"
)

const (
	sessionsPath     = "/api/browser/sessions"
	syncRequestsPath = "/api/browser/sessions/sync-requests"
	undoPath         = "/api/browser/sessions/undo"
)

func (h *harness) syncRequest(sites ...string) result {
	h.t.Helper()
	return h.post(syncRequestsPath, wire.SessionSyncRequest{Sites: sites})
}

// answerWhenAsked plays the extension: it checks in until it is handed a
// request, then answers it with the cookies. Every check-in carries the host's
// count, as the extension's would. It stops after the answer.
func (h *harness) answerWhenAsked(host string, cookies ...wire.SessionCookie) <-chan result {
	out := make(chan result, 1)
	go func() {
		for i := 0; i < 500; i++ {
			r := h.checkin(wire.SessionSiteCount{Domain: host, Cookies: 4})
			if p, ok := r.data()["pending"].(map[string]any); ok {
				out <- h.post(importsPath, wire.SessionImport{RequestID: p["requestId"].(string), Cookies: cookies, Skipped: []string{}})
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		out <- result{}
	}()
	return out
}

func ownHostHarness(t *testing.T, b *fakeBrowser, live func() bool, wait time.Duration) *harness {
	t.Helper()
	return newDomainHarness(t, func(c *Config) {
		c.SessionBrowser, c.ApplyRunLive, c.ownHostWait = b, live, wait
	})
}

func TestSessionSyncRequestRoutesAreAdminAndRemoteDenied(t *testing.T) {
	for method, path := range map[string]string{"POST": syncRequestsPath, "GET": sessionsPath} {
		r := routeOf(t, method, path)
		wantAccess := AccessAdmin
		if method == "GET" {
			wantAccess = AccessStandard
		}
		if r.Access != wantAccess || r.Remote != RemoteDenied {
			t.Errorf("%s %s: %+v", method, path, r)
		}
	}
	if r := routeOf(t, "POST", undoPath); r.Access != AccessAdmin || r.Remote != RemoteDenied || r.Response != "SessionUndoResult" {
		t.Errorf("undo: %+v", r)
	}
}

func TestSessionSyncRequestForbiddenDomain(t *testing.T) {
	h := sessionHarness(t, &fakeBrowser{sites: []string{"jobs.example.test"}}, nil)
	for _, site := range []string{"accounts.google.com", "m.youtube.com"} {
		r := h.syncRequest("jobs.example.test", site)
		if r.StatusCode != 403 || r.errReason() != "not-allowed" {
			t.Fatalf("%s: %d %s", site, r.StatusCode, r.body)
		}
	}
	if h.srv.signin.pending != nil {
		t.Fatal("nothing is queued after a refusal")
	}
}

func TestSessionSyncRequestQueuesListedSites(t *testing.T) {
	h := sessionHarness(t, &fakeBrowser{sites: []string{"jobs.example.test", "example.org"}}, nil)
	r := h.syncRequest("jobs.example.test", "Careers.Jobs.example.test", "example.org")
	if r.StatusCode != 202 || fmt.Sprint(r.data()["domains"]) != "[example.org jobs.example.test]" || r.data()["expiresAt"] == nil {
		t.Fatalf("subdomains collapse into their listed parent: %d %s", r.StatusCode, r.body)
	}
	first := r.data()["requestId"]
	if got := h.checkin(); got.data()["pending"].(map[string]any)["requestId"] != first {
		t.Fatalf("the extension is handed the request: %s", got.body)
	}
	second := h.syncRequest("example.org")
	if second.StatusCode != 202 || second.data()["requestId"] == first {
		t.Fatalf("a newer request replaces the pending one: %d %s", second.StatusCode, second.body)
	}
	for name, sites := range map[string][]string{"not on the list": {"unlisted.example.test"}, "a parent of a listed site": {"example.test"}, "none": {}} {
		r := h.syncRequest(sites...)
		want, reason := 422, "not-listed"
		if len(sites) == 0 {
			reason = ""
		}
		if r.StatusCode != want || r.errReason() != reason {
			t.Errorf("%s: %d %s", name, r.StatusCode, r.body)
		}
	}
	if h.srv.signin.pending == nil || h.srv.signin.pending.domains[0] != "example.org" {
		t.Fatal("a refusal leaves the pending request alone")
	}
}

func TestSessionLiveRunBlocksUserRequestAndUndo(t *testing.T) {
	live := true
	b := &fakeBrowser{sites: []string{"jobs.example.test"}}
	h := sessionHarness(t, b, func() bool { return live })
	if r := h.syncRequest("jobs.example.test"); r.StatusCode != 409 || r.errReason() != "apply-run-live" {
		t.Fatalf("request: %d %s", r.StatusCode, r.body)
	}
	if r := h.post(undoPath, nil); r.StatusCode != 409 || r.errReason() != "apply-run-live" || b.undone != 0 {
		t.Fatalf("undo: %d %s", r.StatusCode, r.body)
	}
	live = false
	if r := h.syncRequest("jobs.example.test"); r.StatusCode != 202 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

// A request the user queued before a run started is not answered while the
// run drives the window: the cookies stay as the run found them, and the
// request waits for the run to end.
func TestSessionLiveRunBlocksTheAnswerToAUserRequest(t *testing.T) {
	live := false
	b := &fakeBrowser{sites: []string{"jobs.example.test"}}
	h := sessionHarness(t, b, func() bool { return live })
	id := h.request("jobs.example.test")
	answer := wire.SessionImport{RequestID: id, Cookies: []wire.SessionCookie{cookie("jobs.example.test", "a")}, Skipped: []string{}}
	live = true
	if r := h.post(importsPath, answer); r.StatusCode != 409 || r.errReason() != "apply-run-live" || b.calls() != 0 {
		t.Fatalf("answer during a run: %d %s, %d imports", r.StatusCode, r.body, b.calls())
	}
	live = false
	if r := h.post(importsPath, answer); r.StatusCode != 200 || b.calls() != 1 {
		t.Fatalf("answer after the run: %d %s, %d imports", r.StatusCode, r.body, b.calls())
	}
}

func TestSessionRequestExpiresAsFailed(t *testing.T) {
	b := &fakeBrowser{sites: []string{"jobs.example.test"}}
	h := sessionHarness(t, b, nil)
	h.syncRequest("jobs.example.test")
	h.clock.advance(sessionRequestTTL + time.Second)
	v := h.get(sessionsPath).data()
	out, _ := v["lastOutcome"].(map[string]any)
	if v["pending"] != nil || out["state"] != "failed" || !strings.Contains(fmt.Sprint(out["message"]), "Chrome did not answer") || b.calls() != 0 {
		t.Fatalf("%v", v)
	}
}

func TestSessionsViewHoldsCountsAndTimesOnly(t *testing.T) {
	b := &fakeBrowser{sites: []string{"jobs.example.test", "example.org"}}
	h := sessionHarness(t, b, nil)
	empty := h.get(sessionsPath)
	if empty.StatusCode != 200 || empty.data()["lastCheckin"] != nil || empty.data()["lastOutcome"] != nil || empty.data()["undoAvailable"] != false {
		t.Fatalf("%d %s", empty.StatusCode, empty.body)
	}
	h.checkin(wire.SessionSiteCount{Domain: "jobs.example.test", Cookies: 7})
	h.syncRequest("example.org")
	r := h.get(sessionsPath)
	v := r.data()
	sites := v["sites"].([]any)
	if len(sites) != 2 {
		t.Fatalf("%s", r.body)
	}
	for _, s := range sites {
		row := s.(map[string]any)
		switch row["domain"] {
		case "jobs.example.test":
			if row["cookies"] != float64(2) || row["sourceCookies"] != float64(7) || row["signIn"] != "unknown" {
				t.Errorf("%v", row)
			}
		case "example.org":
			if row["sourceCookies"] != nil {
				t.Errorf("a site the extension has not counted: %v", row)
			}
		}
	}
	if v["lastCheckin"] == nil || v["pending"] == nil {
		t.Fatalf("%s", r.body)
	}
	id := v["pending"].(map[string]any)["requestId"].(string)
	h.post(importsPath, wire.SessionImport{RequestID: id, Cookies: []wire.SessionCookie{cookie("example.org", "a")}})
	v = h.get(sessionsPath).data()
	out, imp := v["lastOutcome"].(map[string]any), v["lastImport"].(map[string]any)
	if out["state"] != "succeeded" || imp["result"].(map[string]any)["imported"] != float64(1) || v["pending"] != nil || v["undoAvailable"] != true {
		t.Fatalf("%v", v)
	}
	if d, _ := imp["domains"].([]any); len(d) != 1 || d[0] != "example.org" {
		t.Errorf("the last import names the synced sites for Undo: %v", imp["domains"])
	}
	if body := string(h.get(sessionsPath).body); strings.Contains(body, cookieSecret) || strings.Contains(body, `"sid"`) {
		t.Fatal("the view repeated a cookie")
	}
}

func TestSessionUndo(t *testing.T) {
	b := &fakeBrowser{}
	h := sessionHarness(t, b, nil)
	h.post(pushesPath, wire.SessionPush{Site: "example.org", Cookies: []wire.SessionCookie{cookie("example.org", "a")}})
	r := h.post(undoPath, nil)
	if r.StatusCode != 200 || r.data()["restored"] != float64(3) || r.data()["failed"] != float64(0) || b.undone != 1 || strings.Contains(string(r.body), cookieSecret) {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if out := h.get(sessionsPath).data()["lastOutcome"].(map[string]any); out["state"] != "undone" {
		t.Fatalf("%v", out)
	}
	b.undoErr = &browser.Error{Code: browser.CodeNoSnapshot, Message: "there is no import to undo"}
	if r := h.post(undoPath, nil); r.StatusCode != 404 || r.errReason() != "no-snapshot" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

func TestSessionUndoIsRefusedWhileAnImportRuns(t *testing.T) {
	b := &fakeBrowser{hold: make(chan struct{}), entered: make(chan struct{}, 1)}
	h := sessionHarness(t, b, nil)
	done := make(chan result, 1)
	go func() {
		done <- h.post(pushesPath, wire.SessionPush{Site: "example.org", Cookies: []wire.SessionCookie{cookie("example.org", "a")}})
	}()
	<-b.entered
	if r := h.post(undoPath, nil); r.StatusCode != 409 || r.errReason() != "busy" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	close(b.hold)
	<-done
}

func TestSessionBrowserFailuresCrossTheBoundaryTyped(t *testing.T) {
	for _, c := range []struct {
		err       error
		status    int
		code      string
		reason    string
		retryable bool
	}{
		{&browser.Error{Code: browser.CodeSnapshotFailed, Message: "cannot write the undo snapshot", Err: errors.New(cookieSecret)}, 503, "unavailable", "snapshot-failed", true},
		{&browser.Error{Code: browser.CodeChromeMissing, Message: "no Google Chrome"}, 503, "unavailable", "chrome-missing", false},
		{&browser.Error{Code: browser.CodeCookieOutOfScope, Message: "a cookie lies outside the requested domains"}, 422, "validation_failed", "cookie-out-of-scope", false},
	} {
		got := sessionError(c.err).(*apikit.Error)
		if got.Code != wire.ErrorCode(c.code) || got.Details.Reason != c.reason || got.Retryable != c.retryable || strings.Contains(got.Message, cookieSecret) {
			t.Errorf("%v: %+v", c.err, got)
		}
	}
	if err := context.Canceled; sessionError(err) != err {
		t.Fatal("other errors pass through")
	}
}

func TestSessionImportFailureIsRecorded(t *testing.T) {
	b := &failingBrowser{err: &browser.Error{Code: browser.CodeSnapshotFailed, Message: "cannot write the undo snapshot", Err: errors.New(cookieSecret)}}
	h := newDomainHarness(t, func(c *Config) { c.SessionBrowser = b })
	r := h.post(pushesPath, wire.SessionPush{Site: "example.org", Cookies: []wire.SessionCookie{cookie("example.org", "a")}})
	if r.StatusCode != 503 || r.errReason() != "snapshot-failed" || strings.Contains(string(r.body)+h.logText(), cookieSecret) {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	out := h.get(sessionsPath).data()["lastOutcome"].(map[string]any)
	if out["state"] != "failed" || strings.Contains(fmt.Sprint(out), cookieSecret) {
		t.Fatalf("%v", out)
	}
}

type failingBrowser struct {
	fakeBrowser
	err error
}

func (f *failingBrowser) Import(context.Context, []string, []wire.SessionCookie) (wire.SessionImportResult, error) {
	return wire.SessionImportResult{}, f.err
}

func TestSessionOwnHostWaitsForTheAnswer(t *testing.T) {
	b := &fakeBrowser{}
	h := ownHostHarness(t, b, nil, 0)
	h.checkin(wire.SessionSiteCount{Domain: "jobs.example.test", Cookies: 4})
	answered := h.answerWhenAsked("jobs.example.test", cookie("jobs.example.test", "sid"), cookie(".jobs.example.test", "csrf"))
	got, err := h.srv.SyncOwnHost(context.Background(), "jobs.example.test")
	if err != nil || got.Status != OwnHostImported || got.Result.Imported != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	if r := <-answered; r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if fmt.Sprint(b.domains) != "[[jobs.example.test]]" {
		t.Fatalf("exactly the run's host: %v", b.domains)
	}
	if h.srv.signin.pending != nil {
		t.Fatal("the request is finished")
	}
}

func TestSessionOwnHostIsExemptFromTheLiveRunRefusal(t *testing.T) {
	h := ownHostHarness(t, &fakeBrowser{}, func() bool { return true }, 0)
	h.checkin(wire.SessionSiteCount{Domain: "jobs.example.test", Cookies: 4})
	answered := h.answerWhenAsked("jobs.example.test", cookie("jobs.example.test", "sid"))
	got, err := h.srv.SyncOwnHost(context.Background(), "jobs.example.test")
	if err != nil || got.Status != OwnHostImported {
		t.Fatalf("%+v %v", got, err)
	}
	<-answered
}

func TestSessionOwnHostTimeoutWithdrawsTheRequest(t *testing.T) {
	b := &fakeBrowser{}
	h := ownHostHarness(t, b, nil, 100*time.Millisecond)
	h.checkin(wire.SessionSiteCount{Domain: "jobs.example.test", Cookies: 4})
	got, err := h.srv.SyncOwnHost(context.Background(), "jobs.example.test")
	if err != nil || got.Status != OwnHostTimeout {
		t.Fatalf("%+v %v", got, err)
	}
	if h.srv.signin.pending != nil {
		t.Fatal("the request was withdrawn")
	}
}

func TestSessionOwnHostWithdrawnRequestRejectsItsOwnLateAnswer(t *testing.T) {
	b := &fakeBrowser{}
	h := ownHostHarness(t, b, nil, 500*time.Millisecond)
	h.checkin(wire.SessionSiteCount{Domain: "jobs.example.test", Cookies: 4})
	// The request id is read in memory: an HTTP check-in per poll could miss
	// the wait on a loaded machine.
	seen := make(chan string, 1)
	go func() {
		for i := 0; i < 400; i++ {
			h.srv.signin.mu.Lock()
			p := h.srv.signin.pending
			h.srv.signin.mu.Unlock()
			if p != nil {
				seen <- p.id
				return
			}
			time.Sleep(time.Millisecond)
		}
		seen <- ""
	}()
	got, _ := h.srv.SyncOwnHost(context.Background(), "jobs.example.test")
	id := <-seen
	if got.Status != OwnHostTimeout || id == "" {
		t.Fatalf("%+v %q", got, id)
	}
	if late := h.post(importsPath, wire.SessionImport{RequestID: id, Cookies: []wire.SessionCookie{cookie("jobs.example.test", "sid")}}); late.StatusCode != 409 || late.errReason() != "stale-request" || b.calls() != 0 {
		t.Fatalf("%d %s", late.StatusCode, late.body)
	}
}

func TestSessionOwnHostUnavailableQueuesNothing(t *testing.T) {
	h := ownHostHarness(t, &fakeBrowser{}, nil, 0)
	got, err := h.srv.SyncOwnHost(context.Background(), "jobs.example.test")
	if err != nil || got.Status != OwnHostUnavailable || got.Reason != "extension-silent" {
		t.Fatalf("no check-in: %+v %v", got, err)
	}
	h.checkin(wire.SessionSiteCount{Domain: "example.test", Cookies: 0})
	if got, _ = h.srv.SyncOwnHost(context.Background(), "jobs.example.test"); got.Status != OwnHostUnavailable || got.Reason != "no-cookies" {
		t.Fatalf("zero cookies above the host: %+v", got)
	}
	h.checkin(wire.SessionSiteCount{Domain: "jobs.example.test", Cookies: 2})
	h.clock.advance(3 * time.Minute)
	if got, _ = h.srv.SyncOwnHost(context.Background(), "jobs.example.test"); got.Reason != "extension-silent" {
		t.Fatalf("a stale check-in: %+v", got)
	}
	if h.srv.signin.pending != nil {
		t.Fatal("nothing was queued")
	}
	for _, host := range []string{"accounts.google.com", "www.youtube.com", "https://jobs.example.test"} {
		if _, err := h.srv.SyncOwnHost(context.Background(), host); err == nil {
			t.Errorf("%s is refused", host)
		}
	}
}

func TestSessionOwnHostRequestReplacedByTheUser(t *testing.T) {
	b := &fakeBrowser{sites: []string{"example.org"}}
	h := ownHostHarness(t, b, nil, 0)
	h.checkin(wire.SessionSiteCount{Domain: "jobs.example.test", Cookies: 4})
	res := make(chan OwnHostSync, 1)
	go func() {
		got, _ := h.srv.SyncOwnHost(context.Background(), "jobs.example.test")
		res <- got
	}()
	for i := 0; i < 500; i++ {
		h.srv.signin.mu.Lock()
		queued := h.srv.signin.pending != nil
		h.srv.signin.mu.Unlock()
		if queued {
			break
		}
		time.Sleep(time.Millisecond)
	}
	h.syncRequest("example.org")
	select {
	case got := <-res:
		if got.Status != OwnHostReplaced {
			t.Fatalf("%+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the run kept waiting after its request was replaced")
	}
}

func TestSessionOwnHostCancelledByTheRun(t *testing.T) {
	h := ownHostHarness(t, &fakeBrowser{}, nil, 0)
	h.checkin(wire.SessionSiteCount{Domain: "jobs.example.test", Cookies: 4})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	if _, err := h.srv.SyncOwnHost(ctx, "jobs.example.test"); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
	if h.srv.signin.pending != nil {
		t.Fatal("the request was withdrawn")
	}
}

func TestApplySessionsAdaptsTheRunsOwnHostRequestForTheChain(t *testing.T) {
	b := &fakeBrowser{}
	h := ownHostHarness(t, b, nil, 100*time.Millisecond)
	// Silent extension: the chain reads "not available", it does not wait.
	got, err := h.srv.ApplySessions().SyncOwnHost(context.Background(), "jobs.example.test")
	if err != nil || got.Imported || got.Reason == "" {
		t.Fatalf("%+v %v", got, err)
	}
	h.checkin(wire.SessionSiteCount{Domain: "jobs.example.test", Cookies: 4})
	answered := h.answerWhenAsked("jobs.example.test", cookie("jobs.example.test", "sid"))
	got, err = h.srv.ApplySessions().SyncOwnHost(context.Background(), "jobs.example.test")
	if err != nil || !got.Imported {
		t.Fatalf("%+v %v", got, err)
	}
	<-answered
}

const checkPath = "/api/browser/sessions/check"

func TestSessionCheck_ProbesEachSiteAndReportsStateNextActionAndFailures(t *testing.T) {
	at := time.Date(2027, 3, 10, 9, 0, 0, 0, time.UTC)
	b := &fakeBrowser{checked: []browser.SignInCheck{
		{Site: browser.SignInSite{Label: "LinkedIn", Domain: "linkedin.com"}, State: browser.StateSignedOut, CheckedAt: at},
		{Site: browser.SignInSite{Label: "Indeed", Domain: "indeed.com"}, State: browser.StateCaptcha, CheckedAt: at, Cached: true},
		{Site: browser.SignInSite{Label: "Jobright", Domain: "jobright.ai"}, State: browser.StateUnknown, Err: errors.New("net::ERR_CONNECTION_REFUSED at 10.0.0.1")},
	}}
	h := newDomainHarness(t, func(c *Config) { c.SessionBrowser = b })
	r := h.post(checkPath, nil)
	if r.StatusCode != 200 || b.checks != 1 {
		t.Fatalf("%d %s (probes %d)", r.StatusCode, r.body, b.checks)
	}
	rows, _ := r.data()["checks"].([]any)
	if len(rows) != 3 {
		t.Fatalf("%d rows: %s", len(rows), r.body)
	}
	row := func(i int) map[string]any { return rows[i].(map[string]any) }
	if row(0)["state"] != "signed_out" || row(0)["next"] != "needs-user" || row(0)["checkedAt"] == nil {
		t.Fatalf("signed out row %v", row(0))
	}
	if row(1)["state"] != "captcha" || row(1)["next"] != "blocked-by-safety" || row(1)["cached"] != true {
		t.Fatalf("captcha row %v", row(1))
	}
	if row(2)["checkedAt"] != nil || row(2)["error"] != "the site could not be checked" || strings.Contains(string(r.body), "10.0.0.1") {
		t.Fatalf("a failed site must say so without the raw error: %v", row(2))
	}
}

func TestSessionCheck_RouteIsStandardRemoteDeniedAndNoBrowserIsUnavailable(t *testing.T) {
	if r := routeOf(t, "POST", checkPath); r.Access != AccessStandard || r.Remote != RemoteDenied || r.Response != "SignInChecks" {
		t.Fatalf("%+v", r)
	}
	h := newDomainHarness(t)
	if r := h.post(checkPath, nil); r.StatusCode != 503 || r.errReason() != "browser-unavailable" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	h = newDomainHarness(t, func(c *Config) {
		c.SessionBrowser = &fakeBrowser{checkErr: &browser.Error{Code: browser.CodeLaunchTimeout, Message: "the browser did not start"}}
	})
	if r := h.post(checkPath, nil); r.StatusCode != 503 || r.errReason() != string(browser.CodeLaunchTimeout) {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}
