package main

import (
	"sync"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"

	"github.com/egoist/mygo"
)

// activityUserInitiatedAllowingIdleSystemSleep is NSActivityUserInitiated
// without NSActivityIdleSystemSleepDisabled.
const activityUserInitiatedAllowingIdleSystemSleep = 0x00FFFFFF &^ (1 << 20)

// The activity is taken at start, in this macOS-only file, so the other
// platforms need no stub.
func init() {
	holdActivity()
	var menus sync.Once
	prepareWebView = func(w *mygo.Window) {
		reachLinksWithTab(w)
		menus.Do(keepLoadsOutOfContextMenus)
	}
	sessionEnding = quitIsTheSystems
}

// keyAEQuitReason is the attribute ('why?') a quit Apple Event carries when
// the system sends it to log out, restart or shut down.
const keyAEQuitReason = 'w'<<24 | 'h'<<16 | 'y'<<8 | '?'

// quitIsTheSystems reads the quit Apple Event being handled, if any: mygo runs
// the quit listeners while it handles the event, on the main thread. Command-Q,
// the tray's Quit, the Dock's Quit and a script's quit carry no reason.
func quitIsTheSystems() bool {
	ev := objc.ID(objc.GetClass("NSAppleEventManager")).Send(objc.RegisterName("sharedAppleEventManager")).Send(objc.RegisterName("currentAppleEvent"))
	if ev == 0 {
		return false
	}
	why := ev.Send(objc.RegisterName("attributeDescriptorForKeyword:"), uint32(keyAEQuitReason))
	return why != 0 && objc.Send[uint32](why, objc.RegisterName("typeCodeValue")) != 0
}

// pageLoadingItems are the entries of WebKit's own context menu that load the
// page natively, past its "Leave without saving?": Reload on the page's
// background and Open Link on a link (WebKit's item identifiers). View →
// Reload asks the page first.
var pageLoadingItems = []string{"WKMenuItemIdentifierReload", "WKMenuItemIdentifierOpenLink"}

// keepLoadsOutOfContextMenus gives mygo's web view class a
// willOpenMenu:withEvent: that drops those entries, then runs the one it
// inherits. A mygo that defines its own keeps it (AddMethod refuses).
func keepLoadsOutOfContextMenus() {
	cls := objc.GetClass("MyGoWebView")
	if cls == 0 {
		return
	}
	sel := objc.RegisterName("willOpenMenu:withEvent:")
	inherited := objc.ID(cls.SuperClass()).Send(objc.RegisterName("instanceMethodForSelector:"), sel)
	cls.AddMethod(sel, objc.NewIMP(func(self objc.ID, cmd objc.SEL, menu, event objc.ID) {
		dropMenuItems(menu, pageLoadingItems)
		if inherited != 0 {
			purego.SyscallN(uintptr(inherited), uintptr(self), uintptr(cmd), uintptr(menu), uintptr(event))
		}
	}), "v@:@@")
}

// dropMenuItems removes the items with these identifiers from menu, and then
// any separator left at an edge or next to another.
func dropMenuItems(menu objc.ID, identifiers []string) {
	sel := objc.RegisterName
	count := func() int { return objc.Send[int](menu, sel("numberOfItems")) }
	item := func(i int) objc.ID { return menu.Send(sel("itemAtIndex:"), i) }
	separator := func(i int) bool { return objc.Send[bool](item(i), sel("isSeparatorItem")) }
	remove := func(i int) { menu.Send(sel("removeItemAtIndex:"), i) }
	for i := count() - 1; i >= 0; i-- {
		id := item(i).Send(sel("identifier"))
		for _, drop := range identifiers {
			if id != 0 && objc.Send[bool](id, sel("isEqualToString:"), nsString(drop)) {
				remove(i)
				break
			}
		}
	}
	after := true // a separator first, or after another, goes
	for i := 0; i < count(); {
		if s := separator(i); s && after {
			remove(i)
		} else {
			after = s
			i++
		}
	}
	if n := count(); n > 0 && separator(n-1) {
		remove(n - 1)
	}
}

func nsString(s string) objc.ID {
	return objc.ID(objc.GetClass("NSString")).Send(objc.RegisterName("stringWithUTF8String:"), s)
}

// holdActivity declares the app's work user-initiated for as long as the
// process lives, so App Nap never moves it to the background priority band
// while its window is hidden; the Mac still sleeps when idle. The server and
// its single SQLite writer run in this process, and a napped app stalled for
// tens of seconds on a busy machine. The returned activity is never ended.
func holdActivity() objc.ID {
	info := objc.ID(objc.GetClass("NSProcessInfo")).Send(objc.RegisterName("processInfo"))
	reason := objc.ID(objc.GetClass("NSString")).Send(objc.RegisterName("stringWithUTF8String:"), "Applysmith runs its server")
	return info.Send(objc.RegisterName("beginActivityWithOptions:reason:"), uint64(activityUserInitiatedAllowingIdleSystemSleep), reason).Send(objc.RegisterName("retain"))
}

// reachLinksWithTab lets Tab move focus to links in the window's page. WebKit
// skips links in the Tab order unless WKPreferences.tabFocusesLinks is set
// (Safari's default, which a WKWebView inherits), so a keyboard user could
// reach every button but no link. mygo does not set it, so it is set on the
// live web view, found among the window's views.
func reachLinksWithTab(w *mygo.Window) {
	mygo.RunOnMain(func() {
		window := objc.ID(w.NativeHandle())
		if window == 0 {
			return
		}
		tabReachesLinksIn(window.Send(objc.RegisterName("contentView")))
	})
}

// tabReachesLinksIn sets the preference on the web view at or below view.
func tabReachesLinksIn(view objc.ID) {
	if web := findWebView(view); web != 0 {
		web.Send(objc.RegisterName("configuration")).Send(objc.RegisterName("preferences")).Send(objc.RegisterName("setTabFocusesLinks:"), true)
	}
}

// findWebView returns the first WKWebView at or below view, or 0.
func findWebView(view objc.ID) objc.ID {
	if view == 0 {
		return 0
	}
	if view.Send(objc.RegisterName("isKindOfClass:"), objc.GetClass("WKWebView")) != 0 {
		return view
	}
	subviews := view.Send(objc.RegisterName("subviews"))
	for i := range int(subviews.Send(objc.RegisterName("count"))) {
		if web := findWebView(subviews.Send(objc.RegisterName("objectAtIndex:"), uint64(i))); web != 0 {
			return web
		}
	}
	return 0
}
