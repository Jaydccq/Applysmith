package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/egoist/mygo"

	"applysmith/internal/extension"
	"applysmith/internal/launch"
	"applysmith/internal/lazyregexp"
	"applysmith/internal/workspace"
)

// Shell is the set of native calls the dashboard may make. The desktop-shell
// capability names them all; every other action goes through the HTTP API.
type Shell struct{ a *shell }

// ShellInfo describes the running shell.
type ShellInfo struct {
	Version  string `json:"version"`
	BuildID  string `json:"buildId"`
	Mode     string `json:"mode"`
	PID      int    `json:"pid"`
	Packaged bool   `json:"packaged"`
	// NotificationsPermitted is false once the system has refused native
	// notifications.
	NotificationsPermitted bool `json:"notificationsPermitted"`
	// LogDir holds the shell and server logs.
	LogDir string `json:"logDir"`
	// StartAtLogin is read from the operating system on every call.
	StartAtLogin bool `json:"startAtLogin"`
	// ExtensionDir is the unpacked extension the app keeps for Chrome to load,
	// current with this build; empty while it could not be written.
	ExtensionDir string `json:"extensionDir"`
}

// GetShellInfo returns the shell's version, build, server ownership, process
// id, log directory, extension folder and the live start-at-login state.
func (s *Shell) GetShellInfo() ShellInfo {
	return ShellInfo{
		Version: mygo.App.Version(), BuildID: launch.BuildID(), Mode: s.a.currentMode(),
		PID: os.Getpid(), Packaged: mygo.App.IsPackaged(), NotificationsPermitted: !s.a.notifyDenied.Load(),
		LogDir: s.a.logDir(), StartAtLogin: s.a.desk.loginEnabled(), ExtensionDir: s.a.keptExtension(),
	}
}

// QuitApp ends the app.
func (s *Shell) QuitApp() { mygo.App.Quit() }

// RevealInFinder shows a file or folder of the workspace in the file manager.
// The path may be absolute or relative to the workspace; anything that
// resolves outside it is refused with path_outside_workspace.
func (s *Shell) RevealInFinder(path string) error {
	real, err := insideWorkspace(s.a.root(), path)
	if err != nil {
		return err
	}
	s.a.desk.showItem(real)
	return nil
}

// SetStartAtLogin turns the login item on or off.
func (s *Shell) SetStartAtLogin(enabled bool) error {
	err := s.a.desk.setLogin(enabled)
	if err != nil {
		s.a.logger().Error("start at login not changed", "enabled", enabled, "err", err)
	}
	return err
}

var extensionShape = lazyregexp.New(`^[A-Za-z0-9]{1,16}$`)

// PickFile asks for one file whose extension is among extensions, written
// with or without the dot. It returns "" when the user cancels.
func (s *Shell) PickFile(ctx context.Context, extensions []string) (string, error) {
	var names []string
	for _, e := range extensions {
		e = trimDot(e)
		if !extensionShape.MatchString(e) {
			return "", errors.New("invalid_extension")
		}
		names = append(names, e)
	}
	if len(names) == 0 {
		return "", errors.New("invalid_extension")
	}
	return s.a.desk.choose(mygo.OpenDialogOptions{
		Parent: mygo.CallerWindow(ctx), Title: "Choose a file",
		Filters: []mygo.FileFilter{{Name: "Files", Extensions: names}},
	})
}

// PickFolder asks for one folder. It returns "" when the user cancels.
func (s *Shell) PickFolder(ctx context.Context) (string, error) {
	return s.a.desk.choose(mygo.OpenDialogOptions{
		Parent: mygo.CallerWindow(ctx), Title: "Choose a folder", Directory: true, CreateDirectories: true,
	})
}

// OpenExternal opens a web or mail address with the default program.
func (s *Shell) OpenExternal(url string) error { return s.a.openLink(url) }

// OpenInApplyWindow opens a posting in the apply window through the server.
func (s *Shell) OpenInApplyWindow(ctx context.Context, url string) error {
	if err := webOnly(url); err != nil {
		s.a.logger().Warn("apply-window link refused")
		return err
	}
	return s.a.callServer(ctx, "/api/browser/apply-window/open", map[string]string{"url": url})
}

// ShowApplyWindow brings the apply window forward through the server.
func (s *Shell) ShowApplyWindow(ctx context.Context) error { return s.a.showApplyWindow(ctx) }

// ReopenDashboard loads the dashboard again in the page that asked, under a
// fresh launch code. The page never sees the code: the shell mints it with its
// own API token and navigates there, so a page whose session ended gets a new
// one without the user hunting for the tray item. Only pages of the server's
// origin can call it (TrustedOrigins).
func (s *Shell) ReopenDashboard(ctx context.Context) error {
	u, err := s.a.freshDashboardURL(ctx)
	if err != nil {
		return err
	}
	return mygo.CallerPage(ctx).LoadURL(u)
}

// SwitchWorkspace has the app show the demo workspace ("demo") or the user's
// own ("home"), by restarting the server it runs on the other folder and
// loading the dashboard there under a fresh session. It returns "started" once
// the switch is under way (the page is replaced a moment later) or "cancelled"
// when nothing changed. The target is one of
// these two words, never a path: the demo is the folder `applysmith demo init`
// makes by default, and it must carry the demo marker. Running work is
// confirmed in a native dialog first. Refused as fixed_port when APPLYSMITH_PORT
// fixes one address for every workspace.
func (s *Shell) SwitchWorkspace(target string) (string, error) { return s.a.beginSwitch(target) }

func trimDot(e string) string {
	for len(e) > 0 && e[0] == '.' {
		e = e[1:]
	}
	return e
}

// root is the workspace folder, empty before it is known.
func (a *shell) root() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.workspaceRoot
}

// keepExtension keeps the unpacked extension in the user data directory
// current with this build, for the user to load in Chrome from Settings,
// Browser extension. Each startup attempt checks it; a folder of this build
// is left alone, so Chrome finds a new build there after an app update.
func (a *shell) keepExtension() {
	if a.desk.userDataDir == nil {
		return
	}
	base, err := a.desk.userDataDir()
	if err == nil {
		dir := filepath.Join(base, "extension")
		var wrote bool
		if wrote, err = extension.Keep(dir); err == nil {
			a.mu.Lock()
			a.extensionDir = dir
			a.mu.Unlock()
			if wrote {
				a.logger().Info("wrote this build of the extension into its folder")
			}
			return
		}
	}
	a.logger().Warn("the extension folder was not written", "err", err)
}

func (a *shell) keptExtension() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.extensionDir
}

func (a *shell) logDir() string {
	if r := a.root(); r != "" {
		return filepath.Join(workspace.Open(r).StateDir(), "logs")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.earlyLogDir
}
