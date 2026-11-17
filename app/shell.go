package main

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"

	"applysmith/internal/api"
	"applysmith/internal/launch"
	"applysmith/internal/notify/stream"
	"applysmith/internal/vault"
	"applysmith/internal/workspace"
	"applysmith/web"
)

const attachWait = 15 * time.Second

// prepareWebView makes Tab reach links in a new window's page and keeps the
// page's context menu from reloading it. Only macOS has these to change;
// elsewhere it does nothing.
var prepareWebView = func(*mygo.Window) {}

// sessionEnding reports whether the quit under way is the system's: a log
// out, restart or shut down. Only macOS tells.
var sessionEnding = func() bool { return false }

// What a window shows until its page paints, as "light-dark(<light>, <dark>)"
// so mygo follows the system appearance, including a change while the app
// runs. Each pair is the page's own --bg (web/src/styles.css, diagnosis.html);
// TestWindowBackgroundsMatchThePages keeps them equal.
const (
	dashboardBackground = "light-dark(#f6f5f1, #161615)"
	diagnosisBackground = "light-dark(#f6f6f4, #1b1b1a)"
)

// Server ownership, as the dashboard and the tray report it.
const (
	modeStarting  = "starting"
	modeInProcess = "in-process"
	modeAttached  = "attached"
	// modeLost is an attached server that stopped answering (watchAttached).
	modeLost  = "lost"
	modeError = "error"
)

// shell is the state of one app run.
type shell struct {
	smoke        bool
	desk         desktop
	quitting     atomic.Bool
	quitChecking atomic.Bool
	mode         atomic.Value // string

	notifyDenied atomic.Bool // the system refused native notifications

	misses   atomic.Int32 // reads of an attached server that failed in a row
	lostTold atomic.Bool  // the user was told the attached server stopped

	mu                sync.Mutex
	win               *mygo.Window
	diag              *mygo.Window // the diagnosis screen, while one exists
	status            *mygo.MenuItem
	pending           *mygo.MenuItem
	restarts          []*mygo.MenuItem // Restart Server in the tray and in the Window menu
	homes             []*mygo.MenuItem // Back to My Workspace, shown only during a visit
	tray              *mygo.Tray
	srv               *api.Server // set only when this app started the server
	log               *slog.Logger
	logWriter         io.WriteCloser
	initialLaunchDone atomic.Bool

	smokeOnce sync.Once // one smoke reader of the diagnosis page per run

	// starting is set while a startup attempt runs: a click on Retry never
	// starts a second one beside it.
	starting atomic.Bool
	// attemptFn replaces the startup sequence (tests).
	attemptFn func()
	ring      lineRing // what this run logged, for a failure before any log file
	logRoot   string   // the workspace whose log file a.log writes to
	// chosen is the workspace picked on the diagnosis screen; it beats the
	// environment and the recorded choice until the app quits.
	chosen string
	// visit is the demo workspace the app shows in place of the home one, set
	// by the dashboard's SwitchWorkspace and held in memory only: home is the
	// workspace the settings, the environment or the default name, and a visit
	// never records itself, so any new process opens home.
	visit string
	// leftPort is the address of the server a switch just stopped, which the
	// next server does not take (launch.Options.AvoidPort).
	leftPort    int
	welcomeRoot string
	earlyLogDir string
	// configDir holds the recorded workspace choice; empty is the user's
	// configuration directory.
	configDir string
	// homeDir locates the demo workspace; empty is the user's home directory.
	homeDir string

	workspaceRoot string
	// extensionDir is the unpacked extension kept for Chrome, set once it
	// holds this build.
	extensionDir  string
	origin        string
	token         func() (string, error) // the local API token, for calls to the server
	notifications *stream.Subscriber
	notifyStop    context.CancelFunc
	notifyDone    chan struct{}
	activityStop  context.CancelFunc
	activityDone  chan struct{}
	activityLabel string
}

func (a *shell) setMode(m string) {
	a.mode.Store(m)
	a.mu.Lock()
	label, restarts := a.activityLabel, slices.Clone(a.restarts)
	a.mu.Unlock()
	for _, it := range restarts {
		it.SetEnabled(m == modeInProcess || m == modeLost)
	}
	a.renderActivity(label, 0, false)
}

func (a *shell) currentMode() string {
	m, _ := a.mode.Load().(string)
	return m
}

func statusLabel(mode string) string {
	switch mode {
	case modeInProcess:
		return "Running"
	case modeAttached:
		return "Attached to a running server"
	case modeLost:
		return "Server not answering"
	case modeError:
		return "Error"
	}
	return "Starting"
}

// begin runs one startup attempt in the background. It refuses to start a
// second while one runs, and reports whether it started.
func (a *shell) begin() bool {
	if !a.starting.CompareAndSwap(false, true) {
		return false
	}
	a.mu.Lock()
	if a.log == nil {
		var out io.Writer = os.Stderr
		if a.desk.startupLogDir != nil {
			if dir, err := a.desk.startupLogDir(); err == nil {
				if f, err := api.OpenLog(filepath.Join(dir, "desktop.log")); err == nil {
					out = f
					a.logWriter = f
					a.earlyLogDir = dir
				}
			}
		}
		a.log = slog.New(slog.NewTextHandler(io.MultiWriter(out, &a.ring), nil))
	}
	a.mu.Unlock()
	go func() {
		defer a.starting.Store(false)
		a.keepExtension()
		a.runStartup(context.Background())
	}()
	return true
}

func (a *shell) runStartup(ctx context.Context) {
	if a.attemptFn != nil {
		a.attemptFn()
		return
	}
	a.attempt(ctx)
}

// attempt is the startup sequence: resolve the workspace, attach to or start
// the server and open the window. A failure shows the diagnosis screen, which
// is the only way it runs again.
func (a *shell) attempt(ctx context.Context) {
	a.setMode(modeStarting)
	opts := workspace.ResolveOptions{Flag: a.workspaceFlag(), ConfigDir: a.configDir}
	configured, err := workspace.HasConfiguredChoice(opts)
	if err != nil {
		a.fail(err)
		return
	}
	stage := diagnosis{Stage: "starting"}
	if !configured {
		root, err := workspace.Resolve(opts)
		if err != nil {
			a.fail(err)
			return
		}
		a.mu.Lock()
		a.welcomeRoot = root
		a.mu.Unlock()
		stage = diagnosis{Stage: "welcome", DefaultPath: root}
	}
	page, err := stage.render()
	if err != nil {
		a.fail(err)
		return
	}
	a.showDiagnosis(page)
	if !configured {
		return
	}
	if err := a.startup(ctx); err != nil {
		a.fail(err)
	}
}

func (a *shell) startup(ctx context.Context) error {
	ws, err := a.openWorkspace()
	if err != nil {
		return err
	}
	opts, err := launchOptions(ws)
	if err != nil {
		return err
	}
	opts.AvoidPort = a.avoidPort()
	sess, err := launch.AttachOrStart(ctx, opts, attachWait)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.origin = sess.Origin
	a.srv = sess.Server
	a.mu.Unlock()
	if sess.Server != nil {
		a.logger().Info("started the server in-process", "origin", sess.Origin)
		a.setMode(modeInProcess)
	} else {
		a.logger().Info("attached to a running server", "origin", sess.Origin)
		a.setMode(modeAttached)
	}
	a.openWindow(sess.Origin, sess.OpenURL)
	a.closeDiagnosis()
	a.recordChosen(ws.Root)
	a.startNotifications(opts, sess.Origin)
	a.startActivity()
	return nil
}

// recordChosen keeps a workspace picked on the diagnosis screen for the next
// launch, once it has started.
func (a *shell) recordChosen(root string) {
	a.mu.Lock()
	chosen, visiting := a.chosen, a.visit != ""
	a.mu.Unlock()
	if chosen == "" || visiting {
		return
	}
	dir := a.configDir
	if dir == "" {
		var err error
		if dir, err = os.UserConfigDir(); err != nil {
			a.logger().Warn("workspace choice not recorded", "err", err)
			return
		}
	}
	if err := workspace.RecordChoice(dir, root); err != nil {
		a.logger().Warn("workspace choice not recorded", "err", err)
	}
}

// workspaceFlag is the folder that beats the environment and the recorded
// choice: the demo during a visit, else the one picked on the diagnosis screen.
func (a *shell) workspaceFlag() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.visit != "" {
		return a.visit
	}
	return a.chosen
}

func (a *shell) avoidPort() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.leftPort
}

func (a *shell) openWorkspace() (*workspace.Workspace, error) {
	flag := a.workspaceFlag()
	root, err := workspace.Resolve(workspace.ResolveOptions{Flag: flag, ConfigDir: a.configDir})
	if err != nil {
		return nil, err
	}
	// The recorded workspace started before, so a missing folder was moved,
	// deleted or is on a drive that is not connected. Init would put an empty
	// workspace in its place, which reads as all the user's data gone.
	if recorded, _ := workspace.RecordedChoice(a.configDir); flag == "" && os.Getenv(workspace.EnvWorkspace) == "" && recorded != "" {
		if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("the workspace folder %s is missing: it was moved, renamed or deleted, or its drive is not connected (%w)", root, fs.ErrNotExist)
		}
	}
	ws := workspace.Open(root)
	if _, err := ws.Init(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.workspaceRoot = ws.Root
	a.mu.Unlock()
	logs := filepath.Join(ws.StateDir(), "logs")
	if err := os.MkdirAll(logs, 0o700); err != nil {
		return nil, err
	}
	a.mu.Lock()
	known := a.logRoot == ws.Root
	a.mu.Unlock()
	if !known {
		f, err := api.OpenLog(filepath.Join(logs, "desktop.log"))
		if err != nil {
			return nil, err
		}
		a.mu.Lock()
		previous := a.logWriter
		a.log, a.logRoot, a.logWriter = slog.New(slog.NewTextHandler(io.MultiWriter(f, &a.ring), nil)), ws.Root, f
		a.mu.Unlock()
		if previous != nil {
			_ = previous.Close()
		}
		a.logger().Info("shell starting", "version", mygo.App.Version(), "packaged", mygo.App.IsPackaged(), "smoke", a.smoke)
	}
	return ws, nil
}

func launchOptions(ws *workspace.Workspace) (launch.Options, error) {
	port, err := desktopPort(os.Getenv)
	if err != nil {
		return launch.Options{}, err
	}
	assets, err := web.Dist()
	if err != nil {
		return launch.Options{}, err
	}
	return launch.Options{
		Workspace: ws, Backend: vault.NewKeychain(), Getenv: os.Getenv,
		Version: mygo.App.Version(), BuildID: launch.BuildID(), Port: port, ReuseLastPort: os.Getenv("APPLYSMITH_PORT") == "", Assets: assets,
	}, nil
}

// Desktop clients discover the listener through the workspace lock, so a
// fixed default port must not prevent another workspace or app from running.
func desktopPort(getenv func(string) string) (int, error) {
	if getenv("APPLYSMITH_PORT") == "" {
		return 0, nil
	}
	return launch.PortFromEnv(getenv)
}

// openWindow loads the dashboard at openURL, which carries the one-time launch
// code. The code is handed over in-process (or read from the private state
// directory when attached), so no other process is given a session.
func (a *shell) openWindow(origin, openURL string) {
	opts := mygo.WindowOptions{
		Title: "Applysmith", URL: openURL, Width: 1440, Height: 900, MinWidth: 960, MinHeight: 640,
		StateKey: "main", BackgroundColor: dashboardBackground,
		// The login mechanism starts the app in the tray only.
		Hidden: a.hiddenAtLogin(),
		// Only pages from the server's own origin may call the bindings.
		Page: mygo.PageOptions{TrustedOrigins: []string{origin}},
	}
	if a.smoke {
		opts.Page.PreloadScript = smokeScript
	}
	w := mygo.NewWindow(opts)
	a.lockToOrigin(w, origin)
	a.reloadWhenGone(w)
	w.OnClose(func(e *mygo.CloseEvent) {
		if !a.quitting.Load() {
			e.PreventDefault()
			w.Hide()
		}
	})
	a.mu.Lock()
	previous, diag := a.win, a.diag
	a.win = w
	a.initialLaunchDone.Store(true)
	a.mu.Unlock()
	if previous != nil && previous != diag && !previous.IsDestroyed() {
		previous.Destroy()
	}
	if a.smoke {
		go a.collectSmoke(w)
	}
}

// fail shows the diagnosis screen and sets the tray to Error. Nothing retries
// from here: the screen's Retry button is the only way back into the startup
// sequence.
func (a *shell) fail(err error) {
	a.logger().Error("startup failed", "err", err)
	a.setMode(modeError)
	page, rerr := a.diagnose(err).render()
	if rerr != nil {
		a.logger().Error("diagnosis screen not rendered", "err", rerr)
		page = "<h1>Applysmith could not start</h1><pre>" + template.HTMLEscapeString(err.Error()) + "</pre>"
	}
	a.showDiagnosis(page)
	if a.smoke {
		a.mu.Lock()
		w := a.diag
		a.mu.Unlock()
		a.smokeOnce.Do(func() { go a.collectDiagnosisSmoke(w) })
	}
}

// diagnose gathers what the screen shows about a failure.
func (a *shell) diagnose(err error) diagnosis {
	port, perr := desktopPort(os.Getenv)
	if perr != nil {
		port = 0
	}
	stateDir := ""
	if root := a.root(); root != "" {
		stateDir = workspace.Open(root).StateDir()
	}
	return diagnose(err, systemInspection(port, stateDir, a.ring.tail()))
}

// showDiagnosis puts the screen in its window, which it creates on first use.
// Closing the window hides it, so the tray can bring it back.
func (a *shell) showDiagnosis(page string) {
	a.mu.Lock()
	w := a.diag
	a.mu.Unlock()
	if w == nil || w.IsDestroyed() {
		w = mygo.NewWindow(mygo.WindowOptions{Title: "Applysmith", Width: 760, Height: 660, MinWidth: 520, MinHeight: 420,
			BackgroundColor: diagnosisBackground,
			// A smoke run reads the page back without a window on the screen.
			Hidden: a.smoke || a.hiddenAtLogin()})
		w.OnClose(func(e *mygo.CloseEvent) {
			if !a.quitting.Load() {
				e.PreventDefault()
				w.Hide()
			}
		})
	}
	a.mu.Lock()
	previous := a.win
	a.diag, a.win = w, w
	a.mu.Unlock()
	if previous != nil && previous != w && !previous.IsDestroyed() {
		previous.Destroy()
	}
	w.Page().LoadHTML(page, "")
	if a.smoke {
		return
	}
	if !a.hiddenAtLogin() {
		w.Show()
	}
}

// reloadWhenGone reloads the dashboard when WebKit's page process ends (a
// crash, or macOS reclaiming a hidden window's memory), which would leave the
// window blank until the user reloads it. A page that ends again within a
// minute is left for the user, so a page that crashes as it loads does not
// reload forever.
func (a *shell) reloadWhenGone(w *mygo.Window) {
	var last time.Time
	w.Page().OnRenderProcessGone(func(reason string) {
		again := time.Since(last) < time.Minute
		a.logger().Warn("dashboard page process ended", "reason", reason, "reloaded", !again)
		if !again {
			last = time.Now()
			go w.Page().Reload()
		}
	})
}

// closeDiagnosis removes the screen once the dashboard is up.
func (a *shell) closeDiagnosis() {
	a.mu.Lock()
	w := a.diag
	a.diag = nil
	a.mu.Unlock()
	if w != nil {
		w.Destroy()
	}
}

// hiddenAtLogin reports whether the login mechanism started the app and the
// main window has neither opened nor been asked for yet: a window made then
// starts hidden, with the app in the tray only.
func (a *shell) hiddenAtLogin() bool {
	return !a.initialLaunchDone.Load() && a.desk.openedAtLogin != nil && a.desk.openedAtLogin()
}

// showWindow brings the main window forward. Before the window exists there is
// nothing to show: it opens shown once startup finishes.
func (a *shell) showWindow() {
	a.initialLaunchDone.Store(true)
	a.mu.Lock()
	w := a.win
	a.mu.Unlock()
	if w == nil {
		return
	}
	if w.IsMinimized() {
		w.Restore()
	}
	w.Show()
	w.Focus()
	// A second launch comes from another process, so this one is not active.
	mygo.App.Focus()
	if a.currentMode() == modeLost {
		go a.tellLost(false)
	}
}

// secondLaunch answers another process started while this one runs. The
// window state is logged because the launching process leaves no other trace.
func (a *shell) secondLaunch() {
	a.showWindow()
	a.mu.Lock()
	w, log := a.win, a.log
	a.mu.Unlock()
	if w == nil {
		return
	}
	// The getters wait for the main thread, which is running this call.
	go func() {
		log.Info("second launch: window shown", "visible", w.IsVisible(), "focused", w.IsFocused())
	}()
}

// stop runs after the event loop ends. It stops a server this app started and
// leaves an attached one running.
func (a *shell) stop() {
	defer func() {
		a.mu.Lock()
		f := a.logWriter
		a.mu.Unlock()
		if f != nil {
			_ = f.Close()
		}
	}()
	a.stopActivity()
	a.stopNotifications()
	a.mu.Lock()
	srv := a.srv
	a.mu.Unlock()
	if srv == nil {
		a.logger().Info("quit; no server of this app to stop", "mode", a.currentMode())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		a.logger().Error("server shutdown", "err", err)
		return
	}
	a.logger().Info("quit; server stopped and lock released")
}

func (a *shell) collectSmoke(w *mygo.Window) {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		res, err := mygo.EvalAs[string](w.Page(), "return JSON.stringify(window.__ajSmoke ?? null)")
		if err == nil && res != "null" {
			a.logger().Info("smoke result", "page", res, "visible", w.IsVisible(), "bounds", fmt.Sprint(w.Bounds()))
			return
		}
	}
	a.logger().Error("smoke result missing")
}

// diagnosisProbe reads the diagnosis page back: whether the bridge is there,
// the cause and the buttons it shows.
const diagnosisProbe = `return JSON.stringify({
  bridge: typeof (window.mygo && window.mygo.call),
  cause: (document.getElementById("cause") || {}).dataset ? document.getElementById("cause").dataset.cause : null,
  heading: document.getElementById("cause") ? document.getElementById("cause").textContent : null,
  buttons: [...document.querySelectorAll("button[data-action]")].map((b) => b.dataset.action + (b.disabled ? "(disabled)" : "")),
  logLines: document.getElementById("log") ? document.getElementById("log").textContent.split("\n").filter(Boolean).length : -1,
  status: document.getElementById("status") ? document.getElementById("status").textContent : null
})`

// collectDiagnosisSmoke logs what the hidden diagnosis page shows, presses
// Retry once and logs the page again.
func (a *shell) collectDiagnosisSmoke(w *mygo.Window) {
	read := func(when string) {
		for range 20 {
			time.Sleep(500 * time.Millisecond)
			res, err := mygo.EvalAs[string](w.Page(), diagnosisProbe)
			if err == nil && res != "" {
				a.logger().Info("smoke diagnosis", "when", when, "page", res, "visible", w.IsVisible())
				return
			}
		}
		a.logger().Error("smoke diagnosis page unreadable", "when", when)
	}
	read("first")
	if _, err := mygo.EvalAs[string](w.Page(), `document.querySelector('button[data-action="Retry"]').click(); return "clicked"`); err != nil {
		a.logger().Error("smoke diagnosis retry not pressed", "err", err)
		return
	}
	time.Sleep(6 * time.Second)
	read("after one Retry")
}
