package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"applysmith/internal/api/wire"
	"applysmith/internal/browser"
	"applysmith/internal/launch"
	"applysmith/internal/store"
	"applysmith/internal/workspace"
)

func TestBrowserCheck_AnswersFromResultsUnderFiveMinutesOldWithoutStartingChrome(t *testing.T) {
	te := newTestEnv(t)
	if o := te.run(context.Background(), "init", "--workspace", te.ws); o.code != 0 {
		t.Fatalf("init: %d %s", o.code, o.stderr)
	}
	ws := workspace.Open(te.ws)
	id, err := ws.ID()
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.OpenWorkspace(context.Background(), te.ws, store.Options{WorkspaceID: id})
	if err != nil {
		t.Fatal(err)
	}
	// Only a signed-in or rate-limited result is answered from the last probe;
	// any other state is read again, which would start Chrome.
	for _, s := range browser.SignInSites {
		state := browser.StateSignedIn
		if s.ID == "indeed" {
			state = browser.StateRateLimited
		}
		if err := launch.SaveSignInState(context.Background(), st, s.Domain, state, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()
	o := te.run(context.Background(), "browser", "check", "--workspace", te.ws)
	if o.code != 0 || !strings.Contains(o.stdout, "LinkedIn") || !strings.Contains(o.stdout, "signed_in") || !strings.Contains(o.stdout, "Indeed         rate_limited (checked") {
		t.Fatalf("%d\n%s\n%s", o.code, o.stdout, o.stderr)
	}
}

func TestBrowserCheck_UsageForAnythingElse(t *testing.T) {
	te := newTestEnv(t)
	for _, args := range [][]string{{"browser"}, {"browser", "restart"}, {"browser", "status", "extra"}, {"browser", "stop", "--json"}, {"browser", "status", "--force"}} {
		if o := te.run(context.Background(), args...); o.code != 2 || !strings.Contains(o.stderr, "browser stop") {
			t.Fatalf("%v: %d %s", args, o.code, o.stderr)
		}
	}
	// With no server for the workspace the live commands say so.
	for _, verb := range []string{"status", "open", "stop"} {
		o := te.run(context.Background(), "browser", verb, "--workspace", te.ws)
		if o.code != 1 || !strings.Contains(o.stderr, "no server runs") {
			t.Fatalf("%s: %d %s", verb, o.code, o.stderr)
		}
	}
}

// fakeBrowserServer answers the browser routes and records the calls.
func fakeBrowserServer(t *testing.T, leased bool) (*liveServer, *[]string) {
	t.Helper()
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, r.Method+" "+r.URL.Path+" "+strings.TrimSpace(string(body)))
		reply := func(code int, v any) {
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(v)
		}
		switch r.URL.Path {
		case "/api/browser/status":
			reply(200, map[string]any{"ok": true, "data": wire.BrowserStatus{State: "running", ChromeVersion: "155.0.0.0",
				Apply: wire.BrowserLane{Window: true, TabCap: 24, Tabs: []wire.BrowserTab{{ID: "T1", URL: "https://jobs.example.test/apply", Active: true, Kept: true}}},
				Scan:  wire.BrowserLane{TabCap: 32, Tabs: []wire.BrowserTab{}}, Events: []wire.BrowserEvent{{Kind: "tab_opened", Detail: "created"}}}})
		case "/api/browser/apply-window/show":
			reply(200, map[string]any{"ok": true, "data": wire.BrowserShown{Shown: true}})
		case "/api/browser/apply-window/open":
			reply(200, map[string]any{"ok": true, "data": wire.BrowserOpened{Opened: true}})
		case "/api/browser/stop":
			if leased && !strings.Contains(string(body), "force") {
				reply(409, wire.Failure{Error: wire.ErrorBody{Code: wire.CodeConflict, Message: "a run holds a lane"}})
				return
			}
			reply(200, map[string]any{"ok": true, "data": wire.BrowserStopped{Outcome: "closed"}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return &liveServer{origin: srv.URL, token: "tok", client: srv.Client()}, &calls
}

func TestBrowserLiveCommands_AreThinOverTheRoutes(t *testing.T) {
	srv, calls := fakeBrowserServer(t, true)
	var out, errw bytes.Buffer
	e := &env{stdout: &out, stderr: &errw}
	api := liveBrowser{e: e, srv: srv}
	ctx := context.Background()

	if code := e.runBrowser(ctx, "status", api, "", false, false); code != 0 || !strings.Contains(out.String(), "browser running (155.0.0.0)") ||
		!strings.Contains(out.String(), "apply lane: window, 1 of 24 tabs") || !strings.Contains(out.String(), "T1 https://jobs.example.test/apply active kept") || !strings.Contains(out.String(), "tab_opened created") {
		t.Fatalf("status %d\n%s%s", code, out.String(), errw.String())
	}
	out.Reset()
	if code := e.runBrowser(ctx, "status", api, "", false, true); code != 0 || !strings.Contains(out.String(), `"state": "running"`) && !strings.Contains(out.String(), `"state":"running"`) {
		t.Fatalf("status --json %d\n%s", code, out.String())
	}
	out.Reset()
	if code := e.runBrowser(ctx, "open", api, "", false, false); code != 0 || !strings.Contains(out.String(), "in front") {
		t.Fatalf("open %d %s", code, out.String())
	}
	if code := e.runBrowser(ctx, "open", api, "https://jobs.example.test/p/1", false, false); code != 0 {
		t.Fatalf("open url %d", code)
	}
	errw.Reset()
	if code := e.runBrowser(ctx, "stop", api, "", false, false); code != 1 || !strings.Contains(errw.String(), "a run holds a lane") || !strings.Contains(errw.String(), "--force") {
		t.Fatalf("stop while leased %d %s", code, errw.String())
	}
	out.Reset()
	if code := e.runBrowser(ctx, "stop", api, "", true, false); code != 0 || !strings.Contains(out.String(), "stopped: closed") {
		t.Fatalf("stop --force %d %s", code, out.String())
	}
	want := []string{
		"GET /api/browser/status", "GET /api/browser/status", "POST /api/browser/apply-window/show {}",
		`POST /api/browser/apply-window/open {"url":"https://jobs.example.test/p/1"}`, "POST /api/browser/stop {}", `POST /api/browser/stop {"force":true}`,
	}
	for n, w := range want {
		if n >= len(*calls) || !strings.HasPrefix((*calls)[n], w) {
			t.Fatalf("call %d: %v, want %s", n, *calls, w)
		}
	}
}
