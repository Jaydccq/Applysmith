package main

import (
	"context"
	"errors"
	"os"

	"github.com/egoist/mygo"

	"applysmith/internal/workspace"
)

// Diagnosis is the set of native calls of the startup diagnosis screen. The
// screen is the shell's own static page; the dashboard is not meant to call
// these, so each one refuses a caller that is not the diagnosis window.
type Diagnosis struct{ a *shell }

// errDiagnosisOnly is why a call from any other page was refused.
var errDiagnosisOnly = errors.New("diagnosis_screen_only")

// fromScreen accepts only a call made by the page of the diagnosis window.
func (a *shell) fromScreen(ctx context.Context) (*mygo.Window, error) {
	w := mygo.CallerWindow(ctx)
	a.mu.Lock()
	ok := w != nil && w == a.diag
	a.mu.Unlock()
	if !ok {
		a.logger().Warn("diagnosis call refused: not from the diagnosis screen")
		return nil, errDiagnosisOnly
	}
	return w, nil
}

// Results the screen understands.
const (
	resultStarted   = "started"
	resultBusy      = "busy"
	resultCancelled = "cancelled"
)

// retry repeats the startup sequence once. It answers "busy" while a start is
// already running.
func (a *shell) retry() string {
	a.logger().Info("startup retried from the diagnosis screen")
	if !a.begin() {
		return resultBusy
	}
	return resultStarted
}

// changeWorkspace asks for a folder, checks it against the workspace rules and
// starts again with it. A folder the rules refuse is reported and nothing
// changes.
func (a *shell) changeWorkspace(parent *mygo.Window) (string, error) {
	picked, err := a.desk.choose(mygo.OpenDialogOptions{Parent: parent, Title: "Choose a workspace folder", Directory: true, CreateDirectories: true})
	if err != nil {
		return "", err
	}
	if picked == "" {
		return resultCancelled, nil
	}
	path, err := workspace.Resolve(workspace.ResolveOptions{Flag: picked, ConfigDir: a.configDir})
	if err != nil {
		a.logger().Warn("workspace choice refused", "err", err)
		return "", err
	}
	a.mu.Lock()
	a.chosen, a.visit = path, ""
	a.mu.Unlock()
	a.logger().Info("workspace changed from the diagnosis screen")
	if !a.begin() {
		return resultBusy, nil
	}
	return resultStarted, nil
}

// openLogs shows the folder that holds the shell and server logs.
func (a *shell) openLogs() error {
	dir := a.logDir()
	if dir == "" {
		return errNoLogs
	}
	if _, err := os.Stat(dir); err != nil {
		return errNoLogs
	}
	return a.desk.openPath(dir)
}

var errNoLogs = errors.New("no_logs_yet")

// Retry repeats the startup sequence once per click.
func (d *Diagnosis) Retry(ctx context.Context) (string, error) {
	if _, err := d.a.fromScreen(ctx); err != nil {
		return "", err
	}
	return d.a.retry(), nil
}

// OpenLogs shows the logs folder.
func (d *Diagnosis) OpenLogs(ctx context.Context) error {
	if _, err := d.a.fromScreen(ctx); err != nil {
		return err
	}
	return d.a.openLogs()
}

// ChangeWorkspace picks another workspace folder and starts with it.
func (d *Diagnosis) ChangeWorkspace(ctx context.Context) (string, error) {
	w, err := d.a.fromScreen(ctx)
	if err != nil {
		return "", err
	}
	return d.a.changeWorkspace(w)
}

// Quit ends the app.
func (d *Diagnosis) Quit(ctx context.Context) error {
	if _, err := d.a.fromScreen(ctx); err != nil {
		return err
	}
	mygo.App.Quit()
	return nil
}

// UseDefault accepts the default candidate from the first-run screen. It is
// never callable by a dashboard page, and no workspace is created before it.
func (d *Diagnosis) UseDefault(ctx context.Context) (string, error) {
	if _, err := d.a.fromScreen(ctx); err != nil {
		return "", err
	}
	return d.a.useDefault()
}

func (a *shell) useDefault() (string, error) {
	a.mu.Lock()
	if a.welcomeRoot == "" {
		a.mu.Unlock()
		return "", errDiagnosisOnly
	}
	a.chosen, a.visit = a.welcomeRoot, ""
	a.mu.Unlock()
	return a.retry(), nil
}
