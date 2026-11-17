package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"applysmith/internal/api"
	"applysmith/internal/api/wire"
	"applysmith/internal/launch"
	"applysmith/internal/notify"
	"applysmith/internal/onboarding"
	"applysmith/internal/workspace"
	"github.com/egoist/mygo"
)

// newTray adds the menu-bar item. It runs on the main thread once the app is
// ready.
func (a *shell) newTray() {
	a.status = &mygo.MenuItem{Label: statusLabel(modeStarting), Disabled: true}
	a.pending = &mygo.MenuItem{Label: "Nothing needs you", Hidden: true, Disabled: true, Click: func(*mygo.MenuItem, *mygo.Window) {
		a.showWindow()
		a.mu.Lock()
		w, origin := a.win, a.origin
		a.mu.Unlock()
		if w != nil && origin != "" {
			go a.openPage(w.Page(), origin+"/runs")
		}
	}}
	tray, err := mygo.NewTray(mygo.TrayOptions{
		Icon: trayIcon(), IconIsTemplate: true, ToolTip: "Applysmith",
		Menu: mygo.NewMenu([]*mygo.MenuItem{
			a.status,
			a.pending,
			mygo.Separator(),
			a.openDashboardItem(),
			{Label: "Show Apply Window", Click: func(*mygo.MenuItem, *mygo.Window) { go a.trayShowApplyWindow() }},
			a.restartItem(),
			a.homeItem(),
			{Label: "View Logs", Click: func(*mygo.MenuItem, *mygo.Window) { go a.viewLogs() }},
			mygo.Separator(),
			{Label: "Quit", Click: func(*mygo.MenuItem, *mygo.Window) { mygo.App.Quit() }},
		}),
	})
	if err != nil {
		log.Println("tray:", err)
	}
	a.mu.Lock()
	a.tray = tray
	a.mu.Unlock()
}

func (a *shell) activity(ctx context.Context) (wire.BoardSummary, error) {
	var data struct {
		Working  *wire.SafeInt `json:"working"`
		NeedsYou *wire.SafeInt `json:"needsYou"`
		Blocked  *wire.SafeInt `json:"blocked"`
	}
	err := a.readServer(ctx, "/api/runs/summary", &data)
	if err != nil {
		return wire.BoardSummary{}, err
	}
	if data.Working == nil || data.NeedsYou == nil || data.Blocked == nil || *data.Working < 0 || *data.NeedsYou < 0 || *data.Blocked < 0 {
		return wire.BoardSummary{}, errors.New("incomplete runs summary")
	}
	return wire.BoardSummary{Working: *data.Working, NeedsYou: *data.NeedsYou, Blocked: *data.Blocked}, nil
}

// startActivity keeps the menu-bar count current. It reads the summary once,
// then again only when the board stream announces a change of the runs, so an
// idle app sends no requests (it polled every second, which ran the keychain
// tool for the token each time and filled the server log). While the stream is
// down the count is read at each reconnect.
func (a *shell) startActivity() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	a.mu.Lock()
	a.activityStop, a.activityDone = cancel, done
	a.mu.Unlock()
	a.misses.Store(0)
	a.lostTold.Store(false)
	kick := make(chan struct{}, 1)
	go func() {
		defer close(done)
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-kick:
				}
				data, err := a.activity(ctx)
				if ctx.Err() != nil || a.quitting.Load() {
					return
				}
				a.updateActivity(data, err)
				a.watchAttached(err)
				// Coalesce a burst of notices into one read.
				select {
				case <-ctx.Done():
					return
				case <-time.After(activityCoalesce):
				}
			}
		}()
		delay := activityMinRetry
		for ctx.Err() == nil && !a.quitting.Load() {
			signal(kick)
			if a.followBoard(ctx, func() { signal(kick) }) {
				delay = activityMinRetry
			} else {
				delay = min(2*delay, activityMaxRetry)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
		}
	}()
}

const (
	activityCoalesce = 250 * time.Millisecond
	activityMinRetry = time.Second
	activityMaxRetry = 30 * time.Second
)

func signal(c chan struct{}) {
	select {
	case c <- struct{}{}:
	default:
	}
}

// followBoard reads the server's board stream until it ends and calls changed
// for every notice about the runs. It reports whether the stream was open.
func (a *shell) followBoard(ctx context.Context, changed func()) bool {
	a.mu.Lock()
	origin, token := a.origin, a.token
	a.mu.Unlock()
	if origin == "" || token == nil {
		return false
	}
	tok, err := token()
	if err != nil {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/api/board/events", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "text/event-stream")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return false
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	event := ""
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			event = ""
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			if event == "reset" {
				changed()
			}
		case strings.HasPrefix(line, "data:") && event == "notice":
			var n wire.BoardNotice
			if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &n) == nil && n.Topic == "runs" {
				changed()
			}
		}
	}
	return true
}

func (a *shell) stopActivity() {
	a.mu.Lock()
	cancel, done := a.activityStop, a.activityDone
	a.mu.Unlock()
	if cancel != nil {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
}

// lostAfter is how many reads in a row must fail before an attached server
// that also fails its health check counts as stopped. A read comes at each
// reconnect of the board stream (1, 2, 4 and 8 s apart), so a server restart
// or a wake from sleep, which the stream survives in seconds, is never shown
// as one.
const lostAfter = 4

// watchAttached follows the reads of a server the app attached to (it cannot
// stop under an in-process one): one that stopped answering is shown as such,
// and one that answers again is attached again, with the page left as it was.
func (a *shell) watchAttached(err error) {
	mode := a.currentMode()
	if mode != modeAttached && mode != modeLost {
		return
	}
	if err == nil {
		a.misses.Store(0)
		if mode == modeLost {
			a.logger().Info("the attached server answers again")
			a.lostTold.Store(false)
			a.setMode(modeAttached)
		}
		return
	}
	if mode == modeLost || a.misses.Add(1) < lostAfter || a.answers() {
		return
	}
	a.logger().Warn("the attached server stopped answering", "err", err)
	a.setMode(modeLost)
	go a.tellLost(false)
}

// answers reports whether the server's health route answers at all; a read
// refused for another reason (the token, an error) is not a stopped server.
func (a *shell) answers() bool {
	a.mu.Lock()
	origin := a.origin
	a.mu.Unlock()
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	_, ok := api.ProbeServer(u.Host)
	return ok
}

// tellLost says that the attached server stopped answering and what can be
// done, once per loss unless again. It never brings a hidden window forward:
// the user may have stopped that server on purpose, so the tray's status line
// says it and the window tells it when it is next shown.
func (a *shell) tellLost(again bool) {
	a.mu.Lock()
	w, origin := a.win, a.origin
	a.mu.Unlock()
	if a.currentMode() != modeLost || a.desk.serverLost == nil || a.desk.shown == nil || !a.desk.shown(w) || (!a.lostTold.CompareAndSwap(false, true) && !again) {
		return
	}
	detail := "Applysmith was using a server started outside the app (applysmith serve) at " + origin + ", and it stopped answering. " +
		"Start that server again and the app reconnects by itself, or choose Restart Server to run this workspace's server inside the app. " +
		"View Logs shows what the server wrote last."
	switch a.desk.serverLost(w, detail) {
	case lostRestart:
		if a.mayLeave(a.windowPage()) {
			a.restartServer()
		}
	case lostLogs:
		a.viewLogs()
	}
}

// The answers of the serverLost dialog.
const (
	lostRestart = iota
	lostLogs
	lostWait
)

func (a *shell) updateActivity(data wire.BoardSummary, err error) {
	label, pending := "", int(data.NeedsYou+data.Blocked)
	if err != nil {
		label, pending = "Activity unavailable", 0
	} else {
		var parts []string
		if data.Working > 0 {
			parts = append(parts, fmt.Sprintf("%d running", data.Working))
		}
		if pending > 0 {
			parts = append(parts, needYou(pending))
		}
		label = strings.Join(parts, " · ")
	}
	a.mu.Lock()
	a.activityLabel = label
	a.mu.Unlock()
	a.renderActivity(label, pending, true)
}

func (a *shell) renderActivity(label string, pending int, badge bool) {
	a.mu.Lock()
	status, tray, item, visit := a.status, a.tray, a.pending, a.visit
	a.mu.Unlock()
	mode := a.currentMode()
	text := statusLabel(mode)
	if visit != "" && (mode == modeInProcess || mode == modeAttached) {
		text += " · demo workspace"
	}
	if label != "" && mode != modeLost {
		text += " · " + label
	}
	if status != nil {
		status.SetLabel(text)
	}
	if badge && item != nil {
		itemLabel := "Nothing needs you"
		if pending > 0 {
			itemLabel = needYou(pending)
		} else if label == "Activity unavailable" {
			itemLabel = "Activity unavailable"
		}
		item.SetLabel(itemLabel)
		item.SetEnabled(pending > 0)
		item.SetVisible(pending > 0)
	}
	if tray != nil {
		title := ""
		if badge && pending > 0 {
			title = fmt.Sprint(pending)
		}
		if badge {
			tray.SetTitle(title)
		}
		tray.SetToolTip("Applysmith — " + text)
	}
	if badge && a.desk.setBadge != nil {
		a.desk.setBadge(pending)
	}
}

func needYou(n int) string {
	if n == 1 {
		return "1 needs you"
	}
	return fmt.Sprintf("%d need you", n)
}

// beforeQuit defers native quit while the page's unsaved-text question, a
// fresh snapshot and the optional dialog run. Only the app's own server stops;
// an attached server outlives its window. A log out, restart or shut down is
// never held: macOS reads a held quit as the app refusing it, and an
// unattended restart would wait for good on a question nobody answers.
func (a *shell) beforeQuit(e *mygo.QuitEvent) {
	if a.quitting.Load() {
		return
	}
	if sessionEnding() {
		a.logger().Info("quit: the session is ending")
		a.quitting.Store(true)
		return
	}
	e.PreventDefault()
	if !a.quitChecking.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer a.quitChecking.Store(false)
		if a.mayLeave(a.windowPage()) && a.allowQuit(context.Background()) {
			a.quitNow()
		}
	}()
}

// quitNow quits without asking.
func (a *shell) quitNow() {
	a.quitting.Store(true)
	a.desk.quit()
}

// quitOnSignal quits when a SIGINT or SIGTERM arrives. The framework turns the
// signal into the same quit a menu choice starts, which the confirmation can
// hold forever on a locked screen; a signal is a deliberate stop, so it is not
// asked about.
func (a *shell) quitOnSignal(sigs <-chan os.Signal) {
	if s := <-sigs; s != nil {
		a.logger().Info("quit on signal", "signal", s.String())
		a.quitNow()
	}
}

func (a *shell) allowQuit(ctx context.Context) bool {
	if a.currentMode() != modeInProcess || a.quitting.Load() {
		return true
	}
	data, err := a.activity(ctx)
	// A signal may have quit while the activity was read.
	if (err == nil && data.Working == 0) || a.quitting.Load() {
		return true
	}
	detail := fmt.Sprintf("%d tasks are still running. Quitting stops this app's server and interrupts that work.", data.Working)
	if err != nil {
		detail = "The server's activity could not be checked. Quitting stops this app's server and may interrupt running work."
	}
	a.mu.Lock()
	w := a.win
	a.mu.Unlock()
	return a.desk.confirmQuit(w, detail)
}

// trayShowApplyWindow is the tray action. A missing Chrome has its own notice;
// any other refusal gets a plain one, since the tray has no page to show it.
func (a *shell) trayShowApplyWindow() {
	if a.currentMode() == modeLost {
		a.tellLost(true)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	err := a.showApplyWindow(ctx)
	var se *serverError
	if err == nil || (errors.As(err, &se) && se.Reason == chromeMissing) {
		return
	}
	a.mu.Lock()
	w := a.win
	a.mu.Unlock()
	a.desk.notice(w, "The apply window could not be shown", err.Error(), []string{"OK"})
}

// viewLogs opens the folder that holds the shell and server logs.
func (a *shell) viewLogs() {
	dir := a.logDir()
	if dir == "" {
		return
	}
	if err := a.desk.openPath(dir); err != nil {
		a.logger().Warn("logs folder not opened", "err", err)
	}
}

// restartServer operates only on the server this shell owns. The startup guard
// also excludes double clicks, Retry and a workspace change during shutdown.
func (a *shell) restartServer() bool {
	if !a.starting.CompareAndSwap(false, true) {
		return false
	}
	defer a.starting.Store(false)
	a.mu.Lock()
	srv, w := a.srv, a.win
	a.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if a.currentMode() == modeLost {
		// The attached server is gone, so there is nothing to stop: the
		// startup attaches to it if it is back and otherwise runs the server
		// in-process.
		a.stopActivity()
		a.stopNotifications()
		a.runStartup(ctx)
		return a.currentMode() != modeError
	}
	if a.currentMode() != modeInProcess || srv == nil {
		return false
	}
	activity, err := a.activity(ctx)
	if err != nil || activity.Working > 0 {
		detail := fmt.Sprintf("%d tasks are running. Restarting interrupts that work.", activity.Working)
		if err != nil {
			detail = "Activity could not be checked. Restarting may interrupt running work."
		}
		if a.desk.confirmRestart == nil || !a.desk.confirmRestart(w, detail) {
			return false
		}
	}
	a.setMode(modeStarting)
	a.stopActivity()
	a.stopNotifications()
	if err := srv.Shutdown(ctx); err != nil {
		a.fail(err)
		return false
	}
	a.mu.Lock()
	a.srv = nil
	a.mu.Unlock()
	a.runStartup(ctx)
	return a.currentMode() != modeError
}

// Targets of SwitchWorkspace.
const (
	targetDemo = "demo"
	targetHome = "home"
)

// Binding errors of SwitchWorkspace, as stable codes.
var (
	errSwitchTarget = errors.New("invalid_workspace_target")
	errDemoMissing  = errors.New("demo_not_found")
	errSwitchBusy   = errors.New("switch_busy")
	errFixedPort    = errors.New("fixed_port")
)

// beginSwitch validates a switch, asks about running work and starts the
// switch in the background, under the same guard as Restart Server and Retry,
// so only one of them runs at a time. It answers resultStarted, or
// resultCancelled when nothing changed: the user kept the running work, or the
// app already shows that workspace.
func (a *shell) beginSwitch(target string) (string, error) {
	visit := ""
	switch target {
	case targetHome:
	case targetDemo:
		root, err := onboarding.ResolveDemo(onboarding.DemoOptions{HomeDir: a.homeDir})
		if err != nil {
			return "", err
		}
		if !workspace.Open(root).IsDemo() {
			return "", errDemoMissing
		}
		visit = root
	default:
		return "", errSwitchTarget
	}
	// One fixed address would serve both workspaces, and the paired extension
	// would send one workspace's token to the other.
	if port, _ := desktopPort(os.Getenv); port != 0 {
		return "", errFixedPort
	}
	if m := a.currentMode(); m != modeInProcess && m != modeAttached {
		return "", errNoServer
	}
	a.mu.Lock()
	same, srv, w := a.visit == visit, a.srv, a.win
	a.mu.Unlock()
	if same {
		return resultCancelled, nil
	}
	if !a.starting.CompareAndSwap(false, true) {
		return "", errSwitchBusy
	}
	// Only a server this app owns is stopped; an attached one keeps running.
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		detail := a.runningWork(ctx)
		cancel()
		if detail != "" && (a.desk.confirmSwitch == nil || !a.desk.confirmSwitch(w, detail)) {
			a.starting.Store(false)
			return resultCancelled, nil
		}
	}
	go func() {
		defer a.starting.Store(false)
		a.switchWorkspace(visit)
	}()
	return resultStarted, nil
}

// switchWorkspace stops the server this app owns, starts on the other folder
// and shows its dashboard. A demo that cannot start leaves the app on the home
// workspace.
func (a *shell) switchWorkspace(visit string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	a.mu.Lock()
	srv, origin := a.srv, a.origin
	a.mu.Unlock()
	a.setMode(modeStarting)
	a.stopActivity()
	a.stopNotifications()
	if srv != nil {
		if err := srv.Shutdown(ctx); err != nil {
			a.fail(err)
			return
		}
		a.mu.Lock()
		a.srv = nil
		a.mu.Unlock()
	}
	a.setVisit(visit, portOf(origin))
	a.runStartup(ctx)
	if visit != "" && a.currentMode() == modeError {
		a.setVisit("", 0)
		a.runStartup(ctx)
		a.mu.Lock()
		w := a.win
		a.mu.Unlock()
		a.desk.notice(w, "The demo workspace could not be opened", "Applysmith is back on your own workspace. The log says why.", []string{"OK"})
	}
	a.mu.Lock()
	a.leftPort = 0
	a.mu.Unlock()
}

// setVisit records which workspace is shown and the port the next server
// avoids, and shows Back to My Workspace only during a visit.
func (a *shell) setVisit(root string, left int) {
	a.mu.Lock()
	a.visit, a.leftPort = root, left
	homes := slices.Clone(a.homes)
	a.mu.Unlock()
	for _, it := range homes {
		it.SetVisible(root != "")
	}
}

func portOf(origin string) int {
	u, err := url.Parse(origin)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(u.Port())
	return n
}

// runningWork says what a switch would interrupt, empty when nothing runs.
// A board that cannot be read counts as running work.
func (a *shell) runningWork(ctx context.Context) string {
	var board wire.BoardEntryList
	if err := a.readServer(ctx, "/api/runs?limit=500", &board); err != nil {
		return "Activity could not be checked. Switching may interrupt running work."
	}
	var titles []string
	for _, e := range board.Entries {
		if e.State == string(notify.Working) {
			titles = append(titles, e.Title)
		}
	}
	if len(titles) == 0 {
		return ""
	}
	shown := titles[:min(len(titles), 3)]
	list := strings.Join(shown, "; ")
	if more := len(titles) - len(shown); more > 0 {
		list += fmt.Sprintf("; and %d more", more)
	}
	return fmt.Sprintf("%d running now: %s. Switching stops this workspace's server and interrupts them.", len(titles), list)
}

// homeItem is Back to My Workspace, in the tray and the Window menu. It is
// hidden except during a visit.
func (a *shell) homeItem() *mygo.MenuItem {
	a.mu.Lock()
	defer a.mu.Unlock()
	it := &mygo.MenuItem{Label: "Back to My Workspace", Hidden: a.visit == "", Click: func(*mygo.MenuItem, *mygo.Window) {
		go func() {
			if !a.mayLeave(a.windowPage()) {
				return
			}
			if _, err := a.beginSwitch(targetHome); err != nil {
				a.mu.Lock()
				w := a.win
				a.mu.Unlock()
				a.desk.notice(w, "Could not switch workspace", err.Error(), []string{"OK"})
			}
		}()
	}}
	a.homes = append(a.homes, it)
	return it
}

// openDashboardItem and restartItem are the entries the tray and the Window
// menu share, so both run the same handlers.
func (a *shell) openDashboardItem() *mygo.MenuItem {
	return &mygo.MenuItem{Label: "Open Dashboard", Accelerator: "CmdOrCtrl+Shift+D", Click: func(*mygo.MenuItem, *mygo.Window) { a.showWindow() }}
}

// restartItem is enabled while this app owns the server or the attached one
// stopped answering (restartServer); setMode keeps every item it made
// current. The page is asked before the restart, which replaces the window.
func (a *shell) restartItem() *mygo.MenuItem {
	m := a.currentMode()
	it := &mygo.MenuItem{Label: "Restart Server", Disabled: m != modeInProcess && m != modeLost, Click: func(*mygo.MenuItem, *mygo.Window) {
		go func() {
			if a.mayLeave(a.windowPage()) {
				a.restartServer()
			}
		}()
	}}
	a.mu.Lock()
	a.restarts = append(a.restarts, it)
	a.mu.Unlock()
	return it
}

// reloadItem stands in for the Reload role, so Reload goes through openPage.
func (a *shell) reloadItem() *mygo.MenuItem {
	return &mygo.MenuItem{Label: "Reload", Accelerator: "CmdOrCtrl+R", Click: func(_ *mygo.MenuItem, w *mygo.Window) {
		if w != nil && w.Page() != nil {
			go a.openPage(w.Page(), "")
		}
	}}
}

// webPage is what the shell asks of a window's page before it replaces it.
type webPage interface {
	EvalContext(context.Context, string) (any, error)
	Reload()
	LoadURL(string) error
}

// leaveWait bounds the wait for the user's answer, so a page that never
// answers (it crashed or navigated while asking) holds no action for good.
const leaveWait = 5 * time.Minute

// mayLeave asks the page before the shell reloads it, loads another address,
// restarts or switches the server under it, or quits: the app's WebKit never
// shows the question a page asks on beforeunload, so a form holding unsaved
// text asks with its own "Leave without saving?" (applysmithHeld and
// applysmithLeave in web/src/router.tsx). It answers true at once when nothing
// holds the page, and for a page without the question (the diagnosis screen,
// a page that failed to load) or one that does not answer within 3 s. On a
// held page it shows the window, so the question is seen even from the tray,
// and answers true only once Leave is chosen.
func (a *shell) mayLeave(p webPage) bool {
	if p == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	held, err := p.EvalContext(ctx, `typeof applysmithHeld === "function" && applysmithHeld()`)
	cancel()
	if err != nil || held != true {
		return true
	}
	a.showWindow()
	ctx, cancel = context.WithTimeout(context.Background(), leaveWait)
	defer cancel()
	leave, err := p.EvalContext(ctx, "applysmithLeave()")
	return err == nil && leave == true
}

// openPage reloads the page, or loads to into it, once mayLeave allows. While
// the attached server is not answering, a load would only swap a page that
// recovers by itself for an error page, so the user is told instead.
func (a *shell) openPage(p webPage, to string) {
	if a.currentMode() == modeLost {
		a.tellLost(true)
		return
	}
	if !a.mayLeave(p) {
		return
	}
	if to == "" {
		p.Reload()
	} else if err := p.LoadURL(to); err != nil {
		a.logger().Warn("page not loaded", "err", err)
	}
}

// windowPage is the page of the window shown now, nil before there is one.
func (a *shell) windowPage() webPage {
	a.mu.Lock()
	w := a.win
	a.mu.Unlock()
	if w == nil {
		return nil
	}
	return w.Page()
}

func (a *shell) applicationMenu() *mygo.Menu {
	return mygo.NewMenu([]*mygo.MenuItem{
		{Label: "Applysmith", Submenu: []*mygo.MenuItem{
			{Label: "About Applysmith", Click: func(*mygo.MenuItem, *mygo.Window) { go a.about() }},
			mygo.Separator(), {Role: mygo.RoleHide}, {Role: mygo.RoleHideOthers}, {Role: mygo.RoleUnhide},
			mygo.Separator(), {Role: mygo.RoleQuit},
		}},
		// Close Window (Command-W), which hides the window.
		{Role: mygo.RoleFileMenu},
		{Role: mygo.RoleEditMenu},
		{Role: mygo.RoleViewMenu, Submenu: []*mygo.MenuItem{
			a.reloadItem(), {Role: mygo.RoleZoomIn}, {Role: mygo.RoleZoomOut}, {Role: mygo.RoleResetZoom}, mygo.Separator(),
			{Role: mygo.RoleToggleFullScreen}, mygo.Separator(),
			{Label: "Show Apply Window", Click: func(*mygo.MenuItem, *mygo.Window) { go a.trayShowApplyWindow() }},
		}},
		{Role: mygo.RoleWindowMenu, Submenu: []*mygo.MenuItem{
			{Role: mygo.RoleMinimize}, {Role: mygo.RoleZoom}, mygo.Separator(),
			a.openDashboardItem(), a.restartItem(), a.homeItem(), mygo.Separator(),
			{Role: mygo.RoleFront},
		}},
		{Label: "Help", Role: mygo.RoleHelp, Submenu: []*mygo.MenuItem{
			{Label: "Open Setup Guide", Click: func(*mygo.MenuItem, *mygo.Window) {
				a.showWindow()
				a.mu.Lock()
				w, origin := a.win, a.origin
				a.mu.Unlock()
				if w != nil && origin != "" {
					go a.openPage(w.Page(), origin+"/setup")
				}
			}},
			{Label: "View Logs", Click: func(*mygo.MenuItem, *mygo.Window) { go a.viewLogs() }},
		}},
	})
}

func (a *shell) about() {
	a.mu.Lock()
	w := a.win
	a.mu.Unlock()
	if a.desk.notice(w, "About Applysmith", "Version "+mygo.App.Version()+"\nBuild "+launch.BuildID(), []string{"View Notices", "OK"}) != 0 {
		return
	}
	dir, err := mygo.App.Path(mygo.PathResources)
	if err == nil {
		err = a.desk.openPath(filepath.Join(dir, "NOTICE"))
	}
	if err != nil {
		a.logger().Warn("notices could not be opened", "err", err)
	}
}
