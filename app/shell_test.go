package main

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestDesktopPortDefaultsToAnAvailableListenerAndHonorsExplicitPorts(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		want    int
		invalid bool
	}{{"", 0, false}, {"47321", 47321, false}, {"bad", 0, true}} {
		t.Run(tc.raw, func(t *testing.T) {
			port, err := desktopPort(func(string) string { return tc.raw })
			if port != tc.want || (err != nil) != tc.invalid {
				t.Fatalf("port=%d, err=%v; want port=%d, invalid=%v", port, err, tc.want, tc.invalid)
			}
		})
	}
}

// The desktop-shell capability names the bindings the dashboard may call; the
// dashboard must find exactly these and nothing else.
func TestShellBindingsAreTheSpecifiedOnes(t *testing.T) {
	typ := reflect.TypeOf(&Shell{})
	var got []string
	for i := range typ.NumMethod() {
		got = append(got, typ.Method(i).Name)
	}
	slices.Sort(got)
	if want := []string{"GetShellInfo", "OpenExternal", "OpenInApplyWindow", "PickFile", "PickFolder", "QuitApp", "ReopenDashboard", "RevealInFinder", "SetStartAtLogin", "ShowApplyWindow", "SwitchWorkspace"}; !slices.Equal(got, want) {
		t.Errorf("bound methods %v, want %v", got, want)
	}
}

// Single instance: a second launch that arrives while the first is still
// starting finds no window and must not panic. (secondLaunch only reads
// a.win; nothing on its path can create one.)
func TestSecondLaunchDuringStartupShowsNothing(t *testing.T) {
	(&shell{}).secondLaunch()
}

// fakePage answers the shell's two questions as a dashboard page would.
type fakePage struct {
	held, leave any
	err         error  // the page does not answer the first question
	waitErr     error  // nor the second
	loaded      string // the address loaded natively, "reload" for a reload
}

func (p *fakePage) EvalContext(_ context.Context, js string) (any, error) {
	if strings.Contains(js, "applysmithHeld") {
		return p.held, p.err
	}
	return p.leave, p.waitErr
}
func (p *fakePage) Reload()                { p.loaded = "reload" }
func (p *fakePage) LoadURL(u string) error { p.loaded = u; return nil }

// The shell reloads or replaces the page only after the user chose Leave, or
// at once when nothing holds the page (or the page cannot say).
func TestOpenPageActsOnlyAfterLeaveOrWhenNothingHoldsThePage(t *testing.T) {
	const runs = "http://127.0.0.1:1/runs"
	for _, tc := range []struct {
		name   string
		page   fakePage
		to     string
		native string
	}{
		{"nothing holds the reload", fakePage{held: false}, "", "reload"},
		{"nothing holds the load", fakePage{held: false}, runs, runs},
		{"unreachable page", fakePage{err: errors.New("timeout")}, runs, runs},
		{"held, Leave", fakePage{held: true, leave: true}, "", "reload"},
		{"held, Stay", fakePage{held: true, leave: false}, runs, ""},
		{"held, no answer", fakePage{held: true, waitErr: context.DeadlineExceeded}, "", ""},
	} {
		(&shell{}).openPage(&tc.page, tc.to)
		if tc.page.loaded != tc.native {
			t.Errorf("%s: loaded natively %q, want %q", tc.name, tc.page.loaded, tc.native)
		}
	}
}
