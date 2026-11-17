// Command app is the Applysmith desktop shell: a window, a tray and a native
// binding over the same server `applysmith serve` runs. It attaches to a server
// that already holds the workspace lock and otherwise runs one in-process.
package main

import (
	"log"
	"os"
	ossignal "os/signal"
	"slices"
	"syscall"

	"github.com/egoist/mygo"

	"applysmith/internal/cli"
	"applysmith/web"
)

func main() {
	// Started under the name applysmith (the symlink Settings installs), the
	// binary is the command line and never reaches the framework, so no
	// window, tray or single-instance lock is involved.
	if cli.InvokedAs(os.Args[0]) {
		cli.Main(cli.Config{Version: mygo.App.Version(), Dashboard: web.Dist})
	}
	// Before anything else, so that a second launch starts no server and no
	// window: the lock hands the launch to the first instance and returns false.
	if !mygo.App.RequestSingleInstanceLock() {
		return
	}
	a := &shell{desk: systemDesktop(), smoke: smokeBuilt && slices.Contains(os.Args[1:], "--smoke")}
	mygo.Bind(&Shell{a}, &Diagnosis{a})
	sigs := make(chan os.Signal, 1)
	ossignal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go a.quitOnSignal(sigs)
	// The app lives in the tray: closing the window hides it.
	mygo.App.OnWindowAllClosed(func() {})
	mygo.App.OnWindowCreated(prepareWebView)
	mygo.App.OnBeforeQuit(a.beforeQuit)
	mygo.App.OnSecondInstance(func([]string, string) { a.secondLaunch() })
	mygo.App.OnNotificationClick(a.notificationClicked)
	mygo.App.OnActivate(func(visible bool) {
		if !visible {
			a.showWindow()
		}
	})
	mygo.App.WhenReady(func() {
		mygo.App.SetMenu(a.applicationMenu())
		a.newTray()
		a.begin()
	})
	err := mygo.App.Run()
	a.stop()
	if err != nil {
		log.Fatal(err)
	}
}
