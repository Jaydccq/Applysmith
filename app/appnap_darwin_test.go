package main

import (
	"slices"
	"testing"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// WebKit's context menu loses the entries that load the page natively
// (identified by WebKit's own names) and keeps the rest, with no separator
// left at an edge or doubled.
func TestContextMenusLoseTheEntriesThatLoadThePage(t *testing.T) {
	// The app loads AppKit when it starts; a test has to.
	if _, err := purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit", purego.RTLD_GLOBAL); err != nil {
		t.Fatal(err)
	}
	sel := objc.RegisterName
	ids := map[string]string{"Reload": "WKMenuItemIdentifierReload", "Open Link": "WKMenuItemIdentifierOpenLink"}
	for _, tc := range []struct{ items, want []string }{
		{[]string{"Back", "Reload"}, []string{"Back"}},
		{[]string{"Reload", "-", "Inspect Element"}, []string{"Inspect Element"}},
		{[]string{"Open Link", "Open Link in New Window", "-", "Copy Link"}, []string{"Open Link in New Window", "-", "Copy Link"}},
		{[]string{"Copy", "-", "Reload", "-", "Look Up"}, []string{"Copy", "-", "Look Up"}},
	} {
		menu := objc.ID(objc.GetClass("NSMenu")).Send(sel("new"))
		for _, title := range tc.items {
			item := objc.ID(objc.GetClass("NSMenuItem")).Send(sel("separatorItem"))
			if title != "-" {
				item = objc.ID(objc.GetClass("NSMenuItem")).Send(sel("new"))
				item.Send(sel("setTitle:"), nsString(title))
				if id, ok := ids[title]; ok {
					item.Send(sel("setIdentifier:"), nsString(id))
				}
			}
			menu.Send(sel("addItem:"), item)
		}
		dropMenuItems(menu, pageLoadingItems)
		var got []string
		for i := range objc.Send[int](menu, sel("numberOfItems")) {
			item := menu.Send(sel("itemAtIndex:"), i)
			if objc.Send[bool](item, sel("isSeparatorItem")) {
				got = append(got, "-")
			} else {
				got = append(got, objc.Send[string](item.Send(sel("title")), sel("UTF8String")))
			}
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%v: left %v, want %v", tc.items, got, tc.want)
		}
	}
}
