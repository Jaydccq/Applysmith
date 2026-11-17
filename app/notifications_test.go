package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"applysmith/internal/api/wire"
	"applysmith/internal/launch"
	"applysmith/internal/onboarding"
	"applysmith/internal/vault"
	"applysmith/internal/workspace"

	"github.com/egoist/mygo"
)

func activityShell(t *testing.T, handler http.HandlerFunc) *shell {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer desktop-test" {
			t.Error("missing authenticated desktop request")
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	a := &shell{origin: srv.URL, token: func() (string, error) { return "desktop-test", nil }}
	a.mode.Store(modeInProcess)
	return a
}

func TestActivityCountsRefreshAndClearAfterResolutionOrFailure(t *testing.T) {
	bodies := []string{`{"working":2,"needsYou":1,"blocked":2}`, `{"working":0,"needsYou":0,"blocked":0}`, `{"working":0,"needsYou":1,"blocked":0}`}
	index := 0
	a := activityShell(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/runs/summary" {
			t.Error(r.URL.Path)
		}
		fmt.Fprintf(w, `{"ok":true,"data":%s}`, bodies[index])
		index++
	})
	var badges []int
	a.desk.setBadge = func(n int) { badges = append(badges, n) }
	for i, want := range []struct {
		label string
		badge int
	}{{"2 running · 3 need you", 3}, {"", 0}, {"1 needs you", 1}} {
		s, err := a.activity(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		a.updateActivity(s, nil)
		if a.activityLabel != want.label || badges[i] != want.badge {
			t.Fatalf("label=%s badges=%v", a.activityLabel, badges)
		}
	}
	a.updateActivity(wire.BoardSummary{}, errors.New("offline"))
	if a.activityLabel != "Activity unavailable" || badges[len(badges)-1] != 0 {
		t.Fatal("stale pending count remained visible")
	}
}

func TestQuitReadsFreshActivityAndKeepsCancellation(t *testing.T) {
	for _, tc := range []struct {
		name, body    string
		status        int
		confirm, want bool
		ask           bool
	}{
		{"idle", `{"working":0,"needsYou":2,"blocked":1}`, 200, false, true, false},
		{"working cancel", `{"working":1,"needsYou":0,"blocked":0}`, 200, false, false, true},
		{"working quit", `{"working":1,"needsYou":0,"blocked":0}`, 200, true, true, true},
		{"offline cancel", `{}`, 503, false, false, true},
		{"malformed cancel", `{}`, 200, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := activityShell(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprintf(w, `{"ok":true,"data":%s}`, tc.body)
			})
			asked := false
			a.desk.confirmQuit = func(_ *mygo.Window, detail string) bool {
				asked = true
				if !strings.Contains(detail, "interrupt") {
					t.Error(detail)
				}
				return tc.confirm
			}
			if got := a.allowQuit(t.Context()); got != tc.want || asked != tc.ask {
				t.Fatalf("quit=%v asked=%v", got, asked)
			}
			if a.quitting.Load() {
				t.Fatal("checking or cancelling a quit must not close windows")
			}
		})
	}
	a := &shell{}
	a.mode.Store(modeAttached)
	if !a.allowQuit(context.Background()) {
		t.Fatal("an attached server keeps running")
	}
}

func TestActivityStartsImmediatelyAndStopsWithTheShell(t *testing.T) {
	a := activityShell(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true,"data":{"working":0,"needsYou":2,"blocked":0}}`)
	})
	badges := make(chan int, 1)
	a.desk.setBadge = func(n int) { badges <- n }
	a.startActivity()
	defer a.stopActivity()
	select {
	case n := <-badges:
		if n != 2 {
			t.Fatal(n)
		}
	case <-time.After(time.Second):
		t.Fatal("initial activity was not loaded")
	}
	a.stopActivity()
	select {
	case <-a.activityDone:
	default:
		t.Fatal("activity subscription did not stop")
	}
}

// The count is read at start and when the board stream says the runs
// changed; an idle app sends no request (it polled every second).
func TestActivityFollowsTheBoardStreamInsteadOfPolling(t *testing.T) {
	var reads atomic.Int32
	notice := make(chan string, 4)
	a := activityShell(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/runs/summary":
			n := reads.Add(1)
			fmt.Fprintf(w, `{"ok":true,"data":{"working":0,"needsYou":%d,"blocked":0}}`, n)
		case "/api/board/events":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: snapshot\ndata: {\"revisions\":{}}\n\n")
			w.(http.Flusher).Flush()
			for {
				select {
				case <-r.Context().Done():
					return
				case topic := <-notice:
					fmt.Fprintf(w, "id: e-1\nevent: notice\ndata: {\"topic\":%q,\"revision\":1}\n\n", topic)
					w.(http.Flusher).Flush()
				}
			}
		default:
			t.Error(r.URL.Path)
		}
	})
	badges := make(chan int, 8)
	a.desk.setBadge = func(n int) { badges <- n }
	a.startActivity()
	defer a.stopActivity()
	wait := func(want int) {
		t.Helper()
		select {
		case n := <-badges:
			if n != want {
				t.Fatalf("badge %d, want %d", n, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("no badge %d", want)
		}
	}
	wait(1)
	time.Sleep(1500 * time.Millisecond)
	if n := reads.Load(); n != 1 {
		t.Fatalf("%d summary reads while nothing changed", n)
	}
	notice <- "tracker"
	notice <- "runs"
	wait(2)
	if n := reads.Load(); n != 2 {
		t.Fatalf("%d summary reads after one runs notice", n)
	}
}

// An attached server that stops is told once, and only after reads failed in a
// row and its health route is silent too: a blip (a restart, a wake) and a
// server that answers but refuses the read are not a stopped server.
func TestAnAttachedServerThatStopsIsToldOnceAndABlipIsNot(t *testing.T) {
	alive := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true,"data":{"status":"ok","apiVersion":1,"workspaceId":"w"}}`)
	}))
	defer alive.Close()
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	a := &shell{origin: gone.URL}
	a.mode.Store(modeAttached)
	told := make(chan string, 4)
	a.desk.shown = func(*mygo.Window) bool { return true }
	a.desk.serverLost = func(_ *mygo.Window, detail string) int { told <- detail; return lostWait }
	refused := errors.New("connection refused")
	fails := func(n int) {
		for range n {
			a.watchAttached(refused)
		}
	}
	fails(lostAfter - 1)
	a.watchAttached(nil)
	fails(lostAfter - 1)
	if a.currentMode() != modeAttached {
		t.Fatal("a blip between answers counted as a stopped server")
	}
	a.origin = alive.URL
	fails(2)
	if a.currentMode() != modeAttached {
		t.Fatal("a server whose health answers counted as stopped")
	}
	a.origin = gone.URL
	a.watchAttached(nil)
	fails(lostAfter)
	if a.currentMode() != modeLost {
		t.Fatalf("mode %s after the server stopped", a.currentMode())
	}
	select {
	case detail := <-told:
		if !strings.Contains(detail, gone.URL) {
			t.Error(detail)
		}
	case <-time.After(time.Second):
		t.Fatal("the user was not told")
	}
	fails(3)
	a.tellLost(false)
	select {
	case <-told:
		t.Fatal("told twice about one stop")
	case <-time.After(100 * time.Millisecond):
	}
	a.watchAttached(nil)
	if a.currentMode() != modeAttached || a.lostTold.Load() {
		t.Fatal("a server that answers again is not attached again")
	}
}

func TestBeforeQuitDefersOnceThenAllowsTheConfirmedNativeQuit(t *testing.T) {
	a := &shell{}
	a.mode.Store(modeAttached)
	quit := make(chan struct{}, 1)
	a.desk.quit = func() { quit <- struct{}{} }
	event := &mygo.QuitEvent{}
	a.beforeQuit(event)
	if !event.DefaultPrevented() {
		t.Fatal("native quit was not held for the activity check")
	}
	select {
	case <-quit:
	case <-time.After(time.Second):
		t.Fatal("approved quit did not reach the native app")
	}
	next := &mygo.QuitEvent{}
	a.beforeQuit(next)
	if next.DefaultPrevented() || !a.quitting.Load() {
		t.Fatal("confirmed quit must close rather than hide the window")
	}
	// A log out or shut down is never held: macOS would read it as a refusal.
	saved := sessionEnding
	t.Cleanup(func() { sessionEnding = saved })
	sessionEnding = func() bool { return true }
	b, logout := &shell{}, &mygo.QuitEvent{}
	b.mode.Store(modeInProcess)
	b.beforeQuit(logout)
	if logout.DefaultPrevented() || !b.quitting.Load() {
		t.Fatal("the system's quit was held")
	}
}

func TestNotificationClickRecoversItsRouteAndFocusesOnlyItsKeptTab(t *testing.T) {
	for _, tc := range []struct {
		name, kind, link       string
		kept                   bool
		boardStatus, tabStatus int
		wantRoute, wantTab     bool
	}{
		{"kept apply", "apply", "/runs/run-1?tab=log", true, 200, 200, true, true},
		{"prior process", "apply", "", true, 200, 200, true, true},
		{"closed tab", "apply", "/runs/run-1", true, 200, 404, true, true},
		{"no kept tab", "apply", "/runs/run-1", false, 200, 200, true, false},
		{"other kind", "evaluate", "/runs/run-1", true, 200, 200, true, false},
		{"board unavailable", "apply", "/runs/run-1", true, 503, 200, true, false},
		{"unknown old click", "apply", "", true, 503, 200, false, false},
		{"foreign link", "apply", "https://outside.example/", true, 200, 200, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			focused := false
			a := activityShell(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" && r.URL.Path == "/api/runs" {
					w.WriteHeader(tc.boardStatus)
					fmt.Fprintf(w, `{"ok":true,"data":{"entries":[{"id":"run-other","kind":"apply","canFocusTab":true,"link":"/runs/run-other"},{"id":"run-1","kind":%q,"canFocusTab":%t,"link":"/runs/run-1"}]}}`, tc.kind, tc.kept)
					return
				}
				if r.Method != "POST" || r.URL.Path != "/api/apply/runs/run-1/show-tab" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
				}
				raw, err := io.ReadAll(r.Body)
				if err != nil || len(raw) != 0 {
					t.Errorf("show-tab requires no request body; got %q, err=%v", raw, err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				focused = true
				w.WriteHeader(tc.tabStatus)
				fmt.Fprint(w, `{"ok":true,"data":{}}`)
			})
			opened := ""
			if err := a.openNotification(t.Context(), "run-1", tc.link, a.origin, func(raw string) error { opened = raw; return nil }); err != nil {
				t.Fatal(err)
			}
			if (opened != "") != tc.wantRoute || focused != tc.wantTab {
				t.Fatalf("opened=%q focused=%v", opened, focused)
			}
			if opened != "" {
				want := tc.link
				if want == "" {
					want = "/runs/run-1"
				}
				if opened != a.origin+want {
					t.Fatal(opened)
				}
			}
		})
	}
}

func TestRestartServerOwnsTheShutdownAndHonorsActiveRunConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name, mode    string
		working       int
		confirm, want bool
	}{
		{"idle owned", modeInProcess, 0, false, true},
		{"active cancel", modeInProcess, 2, false, false},
		{"active restart", modeInProcess, 2, true, true},
		{"attached", modeAttached, 0, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws, backend := newWorkspace(t), vault.NewMemory()
			owned, err := launch.Start(t.Context(), launchOpts(ws, backend))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { owned.Shutdown(context.Background()) })
			a := activityShell(t, func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"ok":true,"data":{"working":%d,"needsYou":0,"blocked":0}}`, tc.working)
			})
			a.srv = owned
			a.mode.Store(tc.mode)
			asked, starts := false, 0
			a.desk.confirmRestart = func(_ *mygo.Window, detail string) bool {
				asked = true
				if !strings.Contains(detail, "interrupt") {
					t.Error(detail)
				}
				return tc.confirm
			}
			a.attemptFn = func() {
				starts++
				next, err := launch.Start(t.Context(), launchOpts(ws, backend))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { next.Shutdown(context.Background()) })
				a.srv = next
				a.mode.Store(modeInProcess)
			}
			if got := a.restartServer(); got != tc.want || starts != boolInt(tc.want) || asked != (tc.mode == modeInProcess && tc.working > 0) {
				t.Fatalf("restart=%v starts=%d asked=%v", got, starts, asked)
			}
			stopped := false
			select {
			case <-owned.Done():
				stopped = true
			default:
			}
			if stopped != tc.want {
				t.Fatalf("original server stopped=%v", stopped)
			}
			a.starting.Store(true)
			if a.restartServer() {
				t.Fatal("an overlapping restart was accepted")
			}
		})
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// A signal is a deliberate stop: the observed hang was SIGTERM turning into the
// same confirmation as a menu Quit, which nobody could answer on a locked screen.
func TestQuitOnSignalQuitsWithoutAskingWhileWorkRuns(t *testing.T) {
	a := activityShell(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("a signal quit must not read the activity")
	})
	quit := make(chan struct{}, 1)
	a.desk.quit = func() { quit <- struct{}{} }
	a.desk.confirmQuit = func(*mygo.Window, string) bool {
		t.Error("a signal quit must not ask")
		return false
	}
	sigs := make(chan os.Signal, 1)
	go a.quitOnSignal(sigs)
	sigs <- syscall.SIGTERM
	select {
	case <-quit:
	case <-time.After(time.Second):
		t.Fatal("SIGTERM did not quit the app")
	}
	if !a.quitting.Load() {
		t.Fatal("the quit was not marked, so the window close would be held back")
	}
	// The framework's own quit, started by the same signal, is then allowed.
	if !a.allowQuit(context.Background()) {
		t.Fatal("the framework's quit after a signal was held for the dialog")
	}
}

// switchRig is an app on a home workspace with a demo workspace beside it. The
// stub answers the runs board; attemptFn starts a real server on whichever
// folder the shell resolves, as the startup sequence does.
type switchRig struct {
	a        *shell
	home     *workspace.Workspace
	demoRoot string
	config   string
	stub     string // origin the shell reads the board from
	flags    []string
	avoided  []int
	notices  []string
}

func newSwitchRig(t *testing.T, board string) *switchRig {
	t.Helper()
	t.Setenv("APPLYSMITH_PORT", "")
	r := &switchRig{home: newWorkspace(t), config: t.TempDir()}
	homeDir := t.TempDir()
	r.demoRoot = filepath.Join(homeDir, "Applysmith-Demo")
	if _, _, err := onboarding.PrepareDemo(r.demoRoot); err != nil {
		t.Fatal(err)
	}
	if err := workspace.RecordChoice(r.config, r.home.Root); err != nil {
		t.Fatal(err)
	}
	backend := vault.NewMemory()
	owned, err := launch.Start(t.Context(), launchOpts(r.home, backend))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { owned.Shutdown(context.Background()) })
	a := activityShell(t, func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprint(w, board)
	})
	r.a, r.stub = a, a.origin
	a.homeDir, a.configDir, a.chosen, a.srv = homeDir, r.config, r.home.Root, owned
	a.desk.notice = func(_ *mygo.Window, title, _ string, _ []string) int { r.notices = append(r.notices, title); return 0 }
	a.attemptFn = func() {
		flag := a.workspaceFlag()
		r.flags = append(r.flags, flag)
		r.avoided = append(r.avoided, a.avoidPort())
		ws := r.home
		if flag != "" && flag != r.home.Root {
			ws = workspace.Open(flag)
		}
		o := launchOpts(ws, backend)
		o.AvoidPort = a.avoidPort()
		next, err := launch.Start(t.Context(), o)
		if err != nil {
			t.Error(err)
			a.setMode(modeError)
			return
		}
		t.Cleanup(func() { next.Shutdown(context.Background()) })
		a.srv = next
		a.setMode(modeInProcess)
		a.recordChosen(ws.Root)
	}
	return r
}

func (r *switchRig) switchTo(t *testing.T, target string) (string, error) {
	t.Helper()
	res, err := r.a.beginSwitch(target)
	for deadline := time.Now().Add(10 * time.Second); r.a.starting.Load() && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
	return res, err
}

func TestSwitchWorkspaceRefusesWhatItCannotDoSafely(t *testing.T) {
	r := newSwitchRig(t, `{"ok":true,"data":{"entries":[]}}`)
	a := r.a
	refused := func(target string, want error) {
		t.Helper()
		if res, err := a.beginSwitch(target); !errors.Is(err, want) || res != "" {
			t.Errorf("%s: %q, %v; want %v", target, res, err, want)
		}
	}
	refused("/etc", errSwitchTarget)
	a.homeDir = t.TempDir()
	refused(targetDemo, errDemoMissing)
	a.homeDir = filepath.Dir(r.demoRoot)
	marker := filepath.Join(workspace.Open(r.demoRoot).StateDir(), workspace.DemoMarkerName)
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	refused(targetDemo, errDemoMissing)
	if err := workspace.Open(r.demoRoot).MarkDemo(); err != nil {
		t.Fatal(err)
	}
	a.mode.Store(modeStarting)
	refused(targetDemo, errNoServer)
	a.mode.Store(modeInProcess)
	a.starting.Store(true)
	refused(targetDemo, errSwitchBusy)
	a.starting.Store(false)
	if res, err := a.beginSwitch(targetHome); err != nil || res != resultCancelled || len(r.flags) != 0 {
		t.Errorf("home while at home must do nothing: %q, %v, starts %v", res, err, r.flags)
	}
	t.Setenv("APPLYSMITH_PORT", "47999")
	refused(targetDemo, errFixedPort)
	if len(r.flags) != 0 {
		t.Errorf("a refused switch started the server: %v", r.flags)
	}
}

// A visit restarts the server on the demo folder and back, never takes the
// address it left, and never touches the recorded home workspace.
func TestSwitchWorkspaceVisitsTheDemoAndComesBackWithoutMovingHome(t *testing.T) {
	r := newSwitchRig(t, `{"ok":true,"data":{"entries":[]}}`)
	a := r.a
	home := a.srv
	a.status = &mygo.MenuItem{}
	if res, err := r.switchTo(t, targetDemo); err != nil || res != resultStarted {
		t.Fatal(res, err)
	}
	if a.status.Label != "Running · demo workspace" {
		t.Errorf("tray status during the visit: %q", a.status.Label)
	}
	if !slices.Equal(r.flags, []string{r.demoRoot}) || a.currentMode() != modeInProcess {
		t.Fatalf("after the switch: flags %v, mode %s", r.flags, a.currentMode())
	}
	if want := portOf(r.stub); r.avoided[0] != want {
		t.Errorf("the demo server avoids %d, want the left address's port %d", r.avoided[0], want)
	}
	select {
	case <-home.Done():
	default:
		t.Error("the home server was not stopped")
	}
	if got, _ := workspace.RecordedChoice(r.config); got != r.home.Root {
		t.Errorf("the recorded home became %q during the visit", got)
	}
	visiting := a.srv
	if res, err := r.switchTo(t, targetDemo); err != nil || res != resultCancelled || len(r.flags) != 1 {
		t.Errorf("a second switch to the demo restarted: %q, %v, %v", res, err, r.flags)
	}
	if res, err := r.switchTo(t, targetHome); err != nil || res != resultStarted {
		t.Fatal(res, err)
	}
	if !slices.Equal(r.flags, []string{r.demoRoot, r.home.Root}) {
		t.Fatalf("flags %v: home is the chosen or recorded folder", r.flags)
	}
	if a.status.Label != "Running" {
		t.Errorf("tray status back home: %q", a.status.Label)
	}
	select {
	case <-visiting.Done():
	default:
		t.Error("the demo server was not stopped")
	}
	if got, _ := workspace.RecordedChoice(r.config); got != r.home.Root {
		t.Errorf("recorded home %q after the visit", got)
	}
	// A new process knows nothing of the visit.
	if fresh := (&shell{configDir: r.config}); fresh.workspaceFlag() != "" {
		t.Error("a visit survives a restart of the app")
	}
}

func TestSwitchWorkspaceNamesRunningWorkAndWaitsForTheAnswer(t *testing.T) {
	r := newSwitchRig(t, `{"ok":true,"data":{"entries":[{"id":"a","title":"Apply to Acme","state":"working"},{"id":"b","title":"Scan","state":"done"},{"id":"c","title":"Evaluate Zed","state":"working"}]}}`)
	var detail string
	answer := false
	r.a.desk.confirmSwitch = func(_ *mygo.Window, d string) bool { detail = d; return answer }
	if res, err := r.switchTo(t, targetDemo); err != nil || res != resultCancelled {
		t.Fatal(res, err)
	}
	if !strings.Contains(detail, "2 running now: Apply to Acme; Evaluate Zed") || strings.Contains(detail, "Scan") || len(r.flags) != 0 {
		t.Fatalf("declined: detail %q, starts %v", detail, r.flags)
	}
	if r.a.currentMode() != modeInProcess || r.a.visit != "" || r.a.starting.Load() {
		t.Fatalf("a declined switch changed the app: mode %s, visit %q", r.a.currentMode(), r.a.visit)
	}
	answer = true
	if res, err := r.switchTo(t, targetDemo); err != nil || res != resultStarted || len(r.flags) != 1 {
		t.Fatalf("confirmed: %q, %v, starts %v", res, err, r.flags)
	}
}

func TestSwitchWorkspaceFallsBackHomeWhenTheDemoWillNotStart(t *testing.T) {
	r := newSwitchRig(t, `{"ok":true,"data":{"entries":[]}}`)
	start := r.a.attemptFn
	r.a.attemptFn = func() {
		if r.a.workspaceFlag() == r.demoRoot {
			r.flags = append(r.flags, r.demoRoot)
			r.a.setMode(modeError)
			return
		}
		start()
	}
	if _, err := r.switchTo(t, targetDemo); err != nil {
		t.Fatal(err)
	}
	if r.a.currentMode() != modeInProcess || r.a.visit != "" || !slices.Equal(r.flags, []string{r.demoRoot, r.home.Root}) {
		t.Fatalf("mode %s, visit %q, flags %v", r.a.currentMode(), r.a.visit, r.flags)
	}
	if len(r.notices) != 1 || !strings.Contains(r.notices[0], "demo workspace could not be opened") {
		t.Errorf("notices %v", r.notices)
	}
}
