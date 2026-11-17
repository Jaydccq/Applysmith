package main

import (
	"errors"

	"github.com/egoist/mygo"
)

// desktop is what the shell asks of the operating system. The bindings call
// it, so their rules are tested without a window.
type desktop struct {
	startupLogDir  func() (string, error)
	userDataDir    func() (string, error)
	showItem       func(path string)
	openPath       func(path string) error
	openExternal   func(url string) error
	choose         func(opts mygo.OpenDialogOptions) (string, error)
	loginEnabled   func() bool
	setLogin       func(on bool) error
	openedAtLogin  func() bool
	copyText       func(text string)
	setBadge       func(int)
	quit           func()
	confirmQuit    func(parent *mygo.Window, detail string) bool
	confirmRestart func(parent *mygo.Window, detail string) bool
	confirmSwitch  func(parent *mygo.Window, detail string) bool
	serverLost     func(parent *mygo.Window, detail string) int // lostRestart, lostLogs or lostWait
	shown          func(w *mygo.Window) bool
	notice         func(parent *mygo.Window, title, detail string, buttons []string) int
}

func systemDesktop() desktop {
	return desktop{
		startupLogDir: func() (string, error) { return mygo.App.Path(mygo.PathLogs) },
		userDataDir:   func() (string, error) { return mygo.App.Path(mygo.PathUserData) },
		showItem:      mygo.Shell.ShowItemInFolder,
		openPath:      mygo.Shell.OpenPath,
		openExternal:  mygo.Shell.OpenExternal,
		choose: func(o mygo.OpenDialogOptions) (string, error) {
			paths, err := mygo.Dialog.Open(o)
			if err != nil || len(paths) == 0 {
				return "", err
			}
			return paths[0], nil
		},
		loginEnabled:  mygo.App.OpenAtLogin,
		setLogin:      mygo.App.SetOpenAtLogin,
		openedAtLogin: mygo.App.WasOpenedAtLogin,
		copyText:      mygo.Clipboard.WriteText,
		setBadge:      mygo.App.SetBadgeCount,
		quit:          mygo.App.Quit,
		confirmQuit: func(parent *mygo.Window, detail string) bool {
			res, err := mygo.Dialog.Message(mygo.MessageOptions{Parent: parent, Type: mygo.MessageWarning,
				Title: "Quit Applysmith?", Message: "Quit Applysmith?", Detail: detail,
				Buttons: []string{"Quit", "Keep Running"}, DefaultButton: 1, CancelButton: 1})
			return err == nil && res.Button == 0
		},
		confirmRestart: func(parent *mygo.Window, detail string) bool {
			res, err := mygo.Dialog.Message(mygo.MessageOptions{Parent: parent, Type: mygo.MessageWarning,
				Title: "Restart Applysmith server?", Message: "Restart Applysmith server?", Detail: detail,
				Buttons: []string{"Restart", "Keep Running"}, DefaultButton: 1, CancelButton: 1})
			return err == nil && res.Button == 0
		},
		confirmSwitch: func(parent *mygo.Window, detail string) bool {
			res, err := mygo.Dialog.Message(mygo.MessageOptions{Parent: parent, Type: mygo.MessageWarning,
				Title: "Switch workspace?", Message: "Switch workspace?", Detail: detail,
				Buttons: []string{"Switch", "Stay Here"}, DefaultButton: 1, CancelButton: 1})
			return err == nil && res.Button == 0
		},
		serverLost: func(parent *mygo.Window, detail string) int {
			res, err := mygo.Dialog.Message(mygo.MessageOptions{Parent: parent, Type: mygo.MessageWarning,
				Title: "The Applysmith server stopped answering", Message: "The Applysmith server stopped answering", Detail: detail,
				Buttons: []string{"Restart Server", "View Logs", "Wait"}, DefaultButton: lostWait, CancelButton: lostWait})
			if err != nil {
				return lostWait
			}
			return res.Button
		},
		shown: func(w *mygo.Window) bool { return w != nil && !w.IsDestroyed() && w.IsVisible() },
		notice: func(parent *mygo.Window, title, detail string, buttons []string) int {
			res, err := mygo.Dialog.Message(mygo.MessageOptions{Parent: parent, Type: mygo.MessageWarning, Title: title, Message: title, Detail: detail, Buttons: buttons})
			if err != nil {
				return len(buttons) - 1
			}
			return res.Button
		},
	}
}

var errNoServer = errors.New("server_not_ready")
