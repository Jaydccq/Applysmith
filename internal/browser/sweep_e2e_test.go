//go:build browser

package browser

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func cookieNamed(t *testing.T, inst *Instance, name string) (Cookie, bool) {
	t.Helper()
	cs, err := inst.Cookies(ctx30(t))
	ok(t, err)
	for _, c := range cs {
		if c.Name == name {
			return c, true
		}
	}
	return Cookie{}, false
}

func aboutThirtyDays(c Cookie) bool {
	d := time.Until(time.Unix(int64(c.Expires), 0))
	return c.Expires > 0 && d > 29*24*time.Hour && d < 31*24*time.Hour
}

func TestE2E_Browser_SessionCookieSweepKeepsScope(t *testing.T) {
	inst, _ := ownInstance(t, nil)
	session := []Cookie{
		{Name: "host_only", Value: "v-host", Domain: "jobs.example.test", Path: "/apply", SameSite: "Lax", HTTPOnly: true},
		{Name: "domain_wide", Value: "v-domain", Domain: ".example.test", Path: "/"},
		{Name: "cross_site", Value: "v-none", Domain: "jobs.example.test", Path: "/", Secure: true, SameSite: "None"},
		{Name: "strict", Value: "v-strict", Domain: "other.example.test", Path: "/", SameSite: "Strict"},
	}
	for _, c := range session {
		ok(t, inst.SetCookie(ctx30(t), c))
	}
	soon := float64(time.Now().Add(72 * time.Hour).Unix())
	ok(t, inst.SetCookie(ctx30(t), Cookie{Name: "persistent", Value: "v-keep", Domain: "jobs.example.test", Path: "/", Expires: soon, SameSite: "Lax"}))
	for _, c := range session {
		if got, _ := cookieNamed(t, inst, c.Name); got.Expires != 0 {
			t.Fatalf("%s was not a session cookie to start with", c.Name)
		}
	}

	n, err := inst.SweepSessionCookies(ctx30(t))
	ok(t, err)
	if n != len(session) {
		t.Fatalf("the sweep rewrote %d cookies, want %d", n, len(session))
	}
	for _, want := range session {
		got, found := cookieNamed(t, inst, want.Name)
		if !found {
			t.Fatalf("%s is gone after the sweep", want.Name)
		}
		if !aboutThirtyDays(got) {
			t.Errorf("%s expires %v, want about 30 days", want.Name, time.Unix(int64(got.Expires), 0))
		}
		if got.Domain != want.Domain || got.Path != want.Path || got.Secure != want.Secure || got.HTTPOnly != want.HTTPOnly || got.SameSite != want.SameSite || got.Value != want.Value {
			t.Errorf("%s changed in the sweep: %+v became %+v", want.Name, want, got)
		}
	}
	kept, _ := cookieNamed(t, inst, "persistent")
	if kept.Expires != soon {
		t.Fatalf("a cookie that already had an expiry was touched: %v", kept.Expires)
	}
	if again, err := inst.SweepSessionCookies(ctx30(t)); err != nil || again != 0 {
		t.Fatalf("a second sweep rewrote %d cookies (%v): swept cookies must be left alone", again, err)
	}
}

// The event for a sweep carries a count and domains, never a name or a value.
func TestE2E_Browser_SweepLogsCountsNotCookies(t *testing.T) {
	inst, o := ownInstance(t, nil)
	for n := range 12 {
		ok(t, inst.SetCookie(ctx30(t), Cookie{Name: fmt.Sprintf("secretname%d", n), Value: fmt.Sprintf("secretvalue%d", n), Domain: "jobs.example.test", Path: "/"}))
	}
	_, err := inst.SweepSessionCookies(ctx30(t))
	ok(t, err)
	if !hasEventWith(inst, EventCookieSweep, "12 cookies: jobs.example.test") {
		t.Fatalf("events %+v", inst.Events())
	}
	raw, _ := os.ReadFile(o.EventLogPath)
	if strings.Contains(string(raw), "secretname") || strings.Contains(string(raw), "secretvalue") {
		t.Fatal("the event log holds a cookie name or value")
	}
}

// A graceful stop sweeps first, so a session cookie is in the profile when the
// next instance starts.
func TestE2E_Browser_SessionCookieSurvivesGracefulRestart(t *testing.T) {
	inst, o := ownInstance(t, nil)
	ok(t, inst.SetCookie(ctx30(t), Cookie{Name: "sid", Value: "v", Domain: "jobs.example.test", Path: "/", SameSite: "Lax"}))
	if out := inst.Stop(ctx30(t)); out != StopClosed {
		t.Fatalf("stop outcome %q", out)
	}
	next := reopen(t, o)
	got, found := cookieNamed(t, next, "sid")
	if !found || !aboutThirtyDays(got) || got.Domain != "jobs.example.test" || got.SameSite != "Lax" {
		t.Fatalf("after the restart: %+v found=%v", got, found)
	}
}

// Chrome commits its cookie store 30 seconds after a change, and not at all when
// the process is killed first. A sweep forces the commit, so a kill right after
// it loses nothing.
func TestE2E_Browser_CookiesSurviveSigkill(t *testing.T) {
	inst, o := ownInstance(t, nil)
	ok(t, inst.SetCookie(ctx30(t), Cookie{Name: "sid", Value: "v", Domain: "jobs.example.test", Path: "/"}))
	_, err := inst.SweepSessionCookies(ctx30(t))
	ok(t, err)
	killBrowser(t, inst)
	eventually(t, 10*time.Second, "the death noticed", func() bool { return inst.deadInfo() != nil })
	next := reopen(t, o)
	got, found := cookieNamed(t, next, "sid")
	if !found || !aboutThirtyDays(got) {
		t.Fatalf("after the kill: %+v found=%v", got, found)
	}
}

func TestBrowser_MarkerCookie_Refresh(t *testing.T) {
	inst, _ := ownInstance(t, func(o *Options) { o.markerEvery = 250 * time.Millisecond })
	first, found := cookieNamed(t, inst, markerName)
	if !found || first.Domain != markerHost || first.Value != "1" || !aboutThirtyDays(first) {
		t.Fatalf("the marker written at launch: %+v found=%v", first, found)
	}
	eventually(t, 10*time.Second, "the marker written again", func() bool {
		c, found := cookieNamed(t, inst, markerName)
		return found && c.Expires > first.Expires
	})
	if n, err := inst.SweepSessionCookies(ctx30(t)); err != nil || n != 0 {
		t.Fatalf("the sweep rewrote %d cookies (%v); the marker is not a session cookie", n, err)
	}
}

// The marker is written at adoption too, not only at launch.
func TestE2E_Browser_MarkerCookieIsWrittenAtAdoption(t *testing.T) {
	inst, o := ownInstance(t, nil)
	marker, found := cookieNamed(t, inst, markerName)
	if !found {
		t.Fatal("no marker after the launch")
	}
	ok(t, inst.DeleteCookie(ctx30(t), marker))
	if _, found := cookieNamed(t, inst, markerName); found {
		t.Fatal("the marker could not be removed for the test")
	}
	openTab(t, inst, LaneApply, "run-marker") // the record is written once both windows exist
	openTab(t, inst, LaneScan, "run-marker")
	pid := inst.PID()
	inst.Detach()
	adopted := reopen(t, o)
	if adopted.PID() != pid {
		t.Fatalf("a new Chrome (%d) was launched instead of adopting %d", adopted.PID(), pid)
	}
	if got, found := cookieNamed(t, adopted, markerName); !found || got.Domain != markerHost {
		t.Fatalf("no marker after the adoption: %+v found=%v", got, found)
	}
}

// Ten idle minutes stop the instance (shortened here): it sweeps cookies,
// closes Chrome, removes its record and the next demand launches a new one. A
// tab left in a window keeps it running. The periodic cookie sweep runs several
// times inside the idle period, as it does in the product (every minute of
// ten), and does not count as use.
func TestE2E_Browser_IdleStopSweepsClosesAndRelaunches(t *testing.T) {
	inst, o := ownInstance(t, func(o *Options) {
		o.idleAfter, o.idleTick, o.sweepEvery = 1500*time.Millisecond, 100*time.Millisecond, 300*time.Millisecond
	})
	ok(t, inst.SetCookie(ctx30(t), Cookie{Name: "sid", Value: "v", Domain: "jobs.example.test", Path: "/"}))
	lease, err := inst.Acquire(LaneApply, "run-idle")
	ok(t, err)
	tab, err := lease.Lane().OpenTab(ctx30(t), "")
	ok(t, err)
	ok(t, lease.Lane().Keep(tab))
	lease.Release()
	// A kept tab is an open tab: the instance stays.
	time.Sleep(3 * time.Second)
	if inst.isStopped() || !processAlive(inst.PID()) {
		t.Fatal("the instance stopped with a tab open in the apply window")
	}
	ok(t, inst.Lane(LaneApply).CloseTab(ctx30(t), tab))
	pid := inst.PID()
	eventually(t, 15*time.Second, "the idle stop", func() bool { return inst.isStopped() })
	eventually(t, 20*time.Second, "Chrome to exit", func() bool { return !processAlive(pid) })
	if _, err := os.Stat(inst.recordPath()); !os.IsNotExist(err) {
		t.Fatalf("the instance record outlived the stop: %v", err)
	}
	if !hasEventWith(inst, EventIdleStop, "") || !hasEventWith(inst, EventCookieSweep, "1 cookies") {
		t.Fatalf("events %+v", inst.Events())
	}
	o.idleAfter, o.idleTick, o.sweepEvery = 0, 0, 0 // the next instance keeps the default periods
	next := reopen(t, o)
	if next.PID() == pid {
		t.Fatal("the next demand did not launch a new instance")
	}
	got, found := cookieNamed(t, next, "sid")
	if !found || !aboutThirtyDays(got) {
		for _, e := range inst.Events() {
			t.Logf("old event %s %q", e.Kind, e.Detail)
		}
		t.Fatalf("the swept cookie after the idle stop: %+v found=%v", got, found)
	}
}
