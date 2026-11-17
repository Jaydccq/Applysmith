//go:build browser

package browser

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"
)

// foreignListener is a loopback listener owned by the test process. It counts
// connections and every byte it receives, to prove nothing is ever sent to it.
type foreignListener struct {
	l     net.Listener
	conns atomic.Int64
	bytes atomic.Int64
}

func newForeignListener(t *testing.T) *foreignListener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &foreignListener{l: l}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			f.conns.Add(1)
			go func() {
				defer c.Close()
				buf := make([]byte, 4096)
				_ = c.SetReadDeadline(time.Now().Add(20 * time.Second))
				for {
					n, err := c.Read(buf)
					f.bytes.Add(int64(n))
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	return f
}

func (f *foreignListener) port() int { return f.l.Addr().(*net.TCPAddr).Port }

func chromeWithPort(port int) []string {
	out, _ := exec.Command("pgrep", "-f", "remote-debugging-port="+strconv.Itoa(port)+" ").Output()
	return strings.Fields(string(out))
}

// chromeMainProcesses lists browser (not helper) processes running profile.
func chromeMainProcesses(profile string) []string {
	out, _ := exec.Command("pgrep", "-f", "Google Chrome --user-data-dir="+profile+" ").Output()
	return strings.Fields(string(out))
}

func TestE2E_Browser_PortStolenBeforeChromeBinds(t *testing.T) {
	dir, err := os.MkdirTemp("", "applysmith-browser-test-")
	ok(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	o := testOptions(t, dir)
	foreign := newForeignListener(t)
	picks := []int{foreign.port()}
	p, err := Launch(ctx30(t), LaunchOptions{
		Chrome: o.Chrome, ProfileDir: o.ProfileDir, Environ: o.Environ, headless: true, timeout: o.launchTimeout,
		pickPort: func() (int, error) {
			if len(picks) > 0 {
				p := picks[0]
				picks = picks[1:]
				return p, nil
			}
			return freePort()
		},
	})
	ok(t, err)
	t.Cleanup(func() { p.terminate() })
	if p.Port == foreign.port() {
		t.Fatal("attached on the stolen port")
	}
	if foreign.bytes.Load() != 0 {
		t.Fatalf("the foreign listener received %d bytes; no request may reach it", foreign.bytes.Load())
	}
	c, err := net.Dial("tcp", foreign.l.Addr().String())
	if err != nil {
		t.Fatalf("the foreign listener was disturbed: %v", err)
	}
	_ = c.Close()
	if pids := chromeWithPort(foreign.port()); len(pids) != 0 {
		t.Fatalf("the child launched on the stolen port is still running: %v", pids)
	}
	if !processAlive(p.PID) {
		t.Fatal("the retried launch is not running")
	}
}

func TestE2E_Browser_ReusedProcessIdentifierRefused(t *testing.T) {
	inst, _ := sharedInstance(t)
	p := inst.proc
	base := Record{PID: p.PID, Start: p.Start, Profile: p.Profile, Port: p.Port, ChromeVersion: p.Version}
	// The control: an honest record is adopted.
	if _, _, err := adopt(ctx30(t), base, inst.opts.Chrome.Major, p.Profile); err != nil {
		t.Fatalf("honest record refused: %v", err)
	}
	cases := map[string]struct {
		mutate func(Record) Record
		code   Code
	}{
		"start time differs": {func(r Record) Record { r.Start = "Thu Jan  1 00:00:00 1970"; return r }, CodeEndpointUnverified},
		"profile differs":    {func(r Record) Record { r.Profile += "-other"; return r }, CodeProfileUnsafe},
		"port differs":       {func(r Record) Record { r.Port = freePortT(t); return r }, CodeEndpointUnverified},
		"pid is not listener": {func(r Record) Record {
			r.PID = os.Getpid()
			return r
		}, CodeEndpointUnverified},
		"pid is gone": {func(r Record) Record { r.PID = deadPID(t); return r }, CodeEndpointUnverified},
	}
	for name, c := range cases {
		_, _, err := adopt(ctx30(t), c.mutate(base), inst.opts.Chrome.Major, p.Profile)
		if err == nil {
			t.Errorf("%s: adoption succeeded", name)
		} else if code, _ := CodeOf(err); code != c.code {
			t.Errorf("%s: code %q, want %q", name, code, c.code)
		}
	}
}

func freePortT(t *testing.T) int {
	t.Helper()
	p, err := freePort()
	ok(t, err)
	return p
}

// Abrupt server death: Chrome keeps running, the next server adopts it with
// the apply tab and lane labels, and closes the scan tabs no run owns.
func TestE2E_Browser_AdoptionAfterAbruptServerDeath(t *testing.T) {
	dir, err := os.MkdirTemp("", "applysmith-browser-test-")
	ok(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	o := testOptions(t, dir)
	first, err := Open(ctx30(t), o)
	ok(t, err)
	t.Cleanup(func() { first.Stop(context.Background()) })
	applyTab, err := first.Lane(LaneApply).OpenTab(ctx30(t), "run-1")
	ok(t, err)
	ok(t, first.Lane(LaneApply).Keep(applyTab))
	for range 3 {
		_, err := first.Lane(LaneScan).OpenTab(ctx30(t), "run-1")
		ok(t, err)
	}
	pid := first.PID()
	first.Detach() // the server dies without stopping Chrome
	if !processAlive(pid) {
		t.Fatal("Chrome died with the server connection")
	}
	second, err := Open(ctx30(t), o)
	ok(t, err)
	t.Cleanup(func() { second.Stop(context.Background()) })
	if second.PID() != pid {
		t.Fatalf("a new Chrome (%d) was launched instead of adopting %d", second.PID(), pid)
	}
	if n := len(second.Lane(LaneApply).Tabs()); n != 1 {
		t.Fatalf("apply lane has %d tabs after adoption, want 1", n)
	}
	if n := len(second.Lane(LaneScan).Tabs()); n != 3 {
		t.Fatalf("scan lane has %d tabs after adoption, want 3", n)
	}
	eventually(t, 10*time.Second, "orphan scan tabs closed within 10 s of adoption", func() bool {
		return len(second.Lane(LaneScan).Tabs()) == 0
	})
	if n := len(second.Lane(LaneApply).Tabs()); n != 1 {
		t.Fatalf("the apply tab did not survive adoption: %d tabs", n)
	}
}

// A lost connection: the CDP websocket drops while Chrome keeps running and
// its endpoint still answers (seen live after 11 hours of a desktop app: every
// tab creation then failed as "timed out after 45s" until the app restarted,
// and the re-attached app showed a stray blank tab in the apply window). The
// instance reports the browser disconnected at once, naming the loss once,
// and the next open adopts the same Chrome with its kept tab and closes the
// blank tab Chrome made for a creation the lost instance never heard back
// from, or whose session setup the drop cut off (flow round 9, E-2: such a tab
// was adopted as the user's); a lease recovers onto it, and an adoption after
// that server dies keeps every tab it held.
func TestE2E_Browser_LostConnectionIsAdopted(t *testing.T) {
	for _, c := range []struct {
		name string
		// givenUp gives a creation up before the connection drops, as one
		// that timed out is, and drops the connection outside any creation;
		// setup drops it once Chrome made the tab, before its session is set
		// up; otherwise it drops while a creation is under way.
		givenUp, setup bool
	}{
		{name: "Chrome makes the tab of the creation the drop cut off"},
		{name: "Chrome makes the tab of a creation given up before the drop", givenUp: true},
		{name: "the drop cuts off the session setup of the tab Chrome made", setup: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, err := os.MkdirTemp("", "applysmith-browser-test-")
			ok(t, err)
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			o := testOptions(t, dir)
			first, err := Open(ctx30(t), o)
			ok(t, err)
			t.Cleanup(func() { first.Stop(context.Background()) })
			kept, err := first.Lane(LaneApply).OpenTab(ctx30(t), "run-1")
			ok(t, err)
			ok(t, first.Lane(LaneApply).Keep(kept))
			lease, err := first.Acquire(LaneApply, "run-2")
			ok(t, err)
			t.Cleanup(lease.Release)
			pid := first.PID()
			var stray target.ID
			drop := func() {
				first.cancelRoot() // what chromedp does when the websocket drops
				// Once the connection is closed nothing more is written to it,
				// so the instance's own command never reaches Chrome; the tab
				// Chrome makes for it is made over another connection.
				select {
				case <-chromedp.FromContext(first.root).Browser.LostConnection:
				case <-time.After(10 * time.Second):
					t.Fatal("the connection did not close")
				}
				stray = madeElsewhere(t, first)
			}
			switch {
			case c.givenUp:
				first.noteAbandoned()
				drop()
				eventually(t, 10*time.Second, "the loss noticed", func() bool { return first.deadInfo() != nil })
			case c.setup:
				// The tab is registered; the record of the loss is written
				// before its session setup goes on.
				setupGate = func(id target.ID) {
					stray = id
					first.cancelRoot()
					eventually(t, 10*time.Second, "the loss recorded", func() bool {
						r, ok := ReadRecord(first.recordPath())
						return ok && r.Abandoned
					})
				}
				t.Cleanup(func() { setupGate = nil })
			default:
				createGate = func(Lane) { drop() }
				t.Cleanup(func() { createGate = nil })
			}
			start := time.Now()
			_, err = first.Lane(LaneApply).OpenTab(ctx30(t), "run-1")
			createGate, setupGate = nil, nil
			if code, _ := CodeOf(err); code != CodeBrowserDisconnected || time.Since(start) > 10*time.Second {
				t.Fatalf("a tab on a lost connection: %v (%s) after %v; want browser-disconnected at once", err, code, time.Since(start))
			}
			if n := strings.Count(err.Error(), string(CodeBrowserDisconnected)); n != 1 {
				t.Fatalf("the failure names the loss %d times: %v", n, err)
			}
			if !processAlive(pid) {
				t.Fatal("Chrome died with the connection")
			}
			second, err := Open(ctx30(t), o)
			ok(t, err)
			t.Cleanup(func() { second.Stop(context.Background()) })
			if second == first || second.PID() != pid {
				t.Fatalf("a new Chrome (%d) was launched instead of adopting %d", second.PID(), pid)
			}
			if tabs := second.Lane(LaneApply).Tabs(); len(tabs) != 1 || tabs[0].ID != kept {
				t.Fatalf("the kept tab did not survive alone: %+v", tabs)
			}
			eventually(t, 10*time.Second, "the stray tab closed", func() bool {
				pages, err := second.listTargets(ctx30(t))
				_, there := pages[stray]
				return err == nil && !there
			})
			made, err := second.Lane(LaneApply).OpenTab(ctx30(t), "run-1")
			if err != nil {
				t.Fatalf("a tab after adoption: %v", err)
			}
			recovered, err := lease.Lane().OpenTab(ctx30(t), "")
			if err != nil {
				t.Fatalf("the lease did not recover onto the adopted browser: %v", err)
			}
			second.Detach() // that server dies too
			third, err := Open(ctx30(t), o)
			ok(t, err)
			t.Cleanup(func() { third.Stop(context.Background()) })
			held := map[string]bool{kept: true, made: true, recovered: true}
			tabs := third.Lane(LaneApply).Tabs()
			for _, tb := range tabs {
				delete(held, tb.ID)
			}
			if third.PID() != pid || len(tabs) != 3 || len(held) != 0 {
				t.Fatalf("the next adoption did not keep the tabs the adopter held: pid %d, tabs %+v", third.PID(), tabs)
			}
		})
	}
}

// madeElsewhere has the browser make, over a connection of its own, the blank
// apply tab that an instance's tab creation asks for. The instance never hears
// of it, as of a tab Chrome made for a command whose answer the dropped
// connection lost.
func madeElsewhere(t *testing.T, i *Instance) target.ID {
	t.Helper()
	alloc, cancelAlloc := remote.NewAllocator(context.Background(), i.proc.wsURL, remote.NoModifyURL)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	cctx, cancelCall := context.WithTimeout(ctx, 10*time.Second)
	defer cancelCall()
	res, err := chromedp.CallBrowser(cctx, target.CreateTarget, target.CreateTargetParams{URL: "about:blank", NewWindow: ptr(false), Focus: ptr(false)})
	ok(t, err)
	return res.TargetID
}

// A lease's tab creation that is the first to find the connection lost has
// acted on no page, so it is made again on the browser the lease's one
// recovery adopts: the run that met the loss goes on. A caller that was
// cancelled meanwhile is told of the loss and spends no recovery.
func TestE2E_Browser_ACreationThatFindsTheConnectionLostIsMadeOnTheRecoveredBrowser(t *testing.T) {
	for _, c := range []struct {
		name      string
		cancelled bool
	}{{name: "the run goes on"}, {name: "a cancelled caller is not retried", cancelled: true}} {
		t.Run(c.name, func(t *testing.T) {
			inst, _ := ownInstance(t, nil)
			lease, err := inst.Acquire(LaneApply, "run-1")
			ok(t, err)
			t.Cleanup(func() {
				if cur := lease.current(); cur != inst {
					endChrome(cur)
				}
			})
			t.Cleanup(lease.Release)
			ctx, cancel := context.WithCancel(ctx30(t))
			defer cancel()
			var lost atomic.Bool
			createGate = func(Lane) {
				if lost.CompareAndSwap(false, true) {
					inst.cancelRoot() // what chromedp does when the websocket drops
					if c.cancelled {
						cancel()
					}
				}
			}
			t.Cleanup(func() { createGate = nil })
			lane := lease.Lane() // taken once, as the apply window takes it
			id, err := lane.OpenTab(ctx, "")
			if c.cancelled {
				if code, _ := CodeOf(err); code != CodeBrowserDisconnected || lease.Recovered() {
					t.Fatalf("a cancelled creation: %v (%s), recovered %v; want browser-disconnected and no recovery", err, code, lease.Recovered())
				}
				return
			}
			if err != nil {
				t.Fatalf("the creation that met the lost connection: %v", err)
			}
			if cur := lease.current(); cur == inst || cur.PID() != inst.PID() || !lease.Recovered() {
				t.Fatalf("the tab was not made on the adopted browser: recovered %v", lease.Recovered())
			}
			if _, err := lane.Evaluate(ctx30(t), id, "1+1"); err != nil {
				t.Fatalf("the new tab does not work: %v", err)
			}
		})
	}
}

// A browser left with no record (a server gone with only the scan window open)
// is adopted through the profile lock instead of failing as profile-in-use. A
// connection lost while the endpoint no longer answers keeps the record, and
// the server stop then stops that browser, unless it holds a kept tab: that one
// stays for the next open to adopt.
func TestE2E_Browser_UnrecordedOrLostBrowserIsAdoptedOrStopped(t *testing.T) {
	dir, err := os.MkdirTemp("", "applysmith-browser-test-")
	ok(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	o := testOptions(t, dir)
	rec := filepath.Join(o.StateDir, "instance.json")
	first, err := Open(ctx30(t), o)
	ok(t, err)
	t.Cleanup(func() { first.Stop(context.Background()) })
	_, err = first.Lane(LaneScan).OpenTab(ctx30(t), "scan-1")
	ok(t, err)
	pid := first.PID()
	first.Detach()
	if _, ok := ReadRecord(rec); ok {
		t.Fatal("a scan-only browser wrote a record; the test needs none")
	}
	second, err := Open(ctx30(t), o)
	ok(t, err)
	t.Cleanup(func() { second.Stop(context.Background()) })
	if second.PID() != pid {
		t.Fatalf("a new Chrome (%d) instead of adopting the unrecorded %d", second.PID(), pid)
	}
	lost := func(i *Instance) {
		i.browserGone(exitInfo{Cause: "connection lost: the debugging endpoint no longer answers"})
	}
	lost(second)
	if r, ok := ReadRecord(rec); !ok || r.PID != pid {
		t.Fatalf("a lost browser that still runs has no record: %+v %v", r, ok)
	}
	if out, err := NewControl(o).Stop(ctx30(t), true); err != nil || out == "" {
		t.Fatalf("the server stop left the lost browser: %q %v", out, err)
	}
	eventually(t, 15*time.Second, "the lost browser exited", func() bool { return !processAlive(pid) })
	if _, ok := ReadRecord(rec); ok {
		t.Fatal("the record outlived the stopped browser")
	}

	third, err := Open(ctx30(t), o)
	ok(t, err)
	t.Cleanup(func() { third.Stop(context.Background()) })
	kept, err := third.Lane(LaneApply).OpenTab(ctx30(t), "run-1")
	ok(t, err)
	ok(t, third.Lane(LaneApply).Keep(kept))
	lost(third)
	if out, _ := NewControl(o).Stop(ctx30(t), true); out != "" || !processAlive(third.PID()) {
		t.Fatalf("a lost browser with a kept tab was stopped: %q", out)
	}
	fourth, err := Open(ctx30(t), o)
	ok(t, err)
	t.Cleanup(func() { fourth.Stop(context.Background()) })
	if tabs := fourth.Lane(LaneApply).Tabs(); fourth.PID() != third.PID() || len(tabs) != 1 || tabs[0].ID != kept {
		t.Fatalf("the kept tab's browser was not adopted: pid %d (want %d), tabs %+v", fourth.PID(), third.PID(), tabs)
	}
}

// A command-line process that launched the browser for one scan tab stops it
// before it exits, so the next command can launch again; a kept tab or an
// adopted browser is left running.
func TestE2E_Browser_StopIdleOnExit(t *testing.T) {
	dir, err := os.MkdirTemp("", "applysmith-browser-test-")
	ok(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	o := testOptions(t, dir)
	first, err := Open(ctx30(t), o)
	ok(t, err)
	t.Cleanup(func() { first.Stop(context.Background()) })
	scan := first.Lane(LaneScan)
	id, err := scan.OpenTab(ctx30(t), "liveness")
	ok(t, err)
	ok(t, scan.CloseTab(ctx30(t), id))
	pid := first.PID()
	if _, stopped := StopIdleOnExit(ctx30(t), o.ProfileDir); !stopped {
		t.Fatal("the browser the command launched was not stopped")
	}
	eventually(t, 15*time.Second, "Chrome exited", func() bool { return !processAlive(pid) })

	second, err := Open(ctx30(t), o)
	ok(t, err)
	t.Cleanup(func() { second.Stop(context.Background()) })
	apply := second.Lane(LaneApply)
	kept, err := apply.OpenTab(ctx30(t), "run-1")
	ok(t, err)
	ok(t, apply.Keep(kept))
	if _, err := second.Lane(LaneScan).OpenTab(ctx30(t), "run-1"); err != nil {
		t.Fatal(err)
	}
	if _, stopped := StopIdleOnExit(ctx30(t), o.ProfileDir); stopped {
		t.Fatal("a browser with a tab kept for the user was stopped")
	}
	second.Detach()
	third, err := Open(ctx30(t), o)
	ok(t, err)
	t.Cleanup(func() { third.Stop(context.Background()) })
	if _, stopped := StopIdleOnExit(ctx30(t), o.ProfileDir); stopped || !processAlive(third.PID()) {
		t.Fatal("an adopted browser was stopped")
	}
}

func TestE2E_Browser_IncompleteDownloadLeavesNoFile(t *testing.T) {
	dir, err := os.MkdirTemp("", "applysmith-browser-test-")
	ok(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	o := testOptions(t, dir)
	fx := newFixtures()
	t.Cleanup(fx.close)
	inst, err := Open(ctx30(t), o)
	ok(t, err)
	t.Cleanup(func() { inst.Stop(context.Background()) })
	tab, err := inst.Lane(LaneApply).OpenTab(ctx30(t), "run-slow")
	ok(t, err)
	navigate(t, inst, LaneApply, tab, fx.Loopback("/dl/link"))
	gesture(t, inst, LaneApply, tab, `const a=document.getElementById('a');a.href='/dl/slow';a.click()`)
	eventually(t, 10*time.Second, "download in progress in staging", func() bool { return len(dirNames(inst.stagingDir())) > 0 })
	out := inst.Stop(context.Background())
	t.Logf("stop outcome %s", out)
	if left := dirNames(o.DownloadsDir); len(left) != 0 {
		t.Fatalf("Downloads holds %v after an interrupted download", left)
	}
}

// A run launches Chrome and opens windows in both lanes without taking the
// foreground from the user's application.
func TestE2E_Browser_LaunchDoesNotStealFocus(t *testing.T) {
	if screenLocked() {
		t.Skip("screen locked: the frontmost-application check cannot be made")
	}
	dir, err := os.MkdirTemp("", "applysmith-browser-test-")
	ok(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	before := frontApp()
	inst, err := Open(ctx30(t), headedOptions(t, dir))
	ok(t, err)
	t.Cleanup(func() { inst.Stop(context.Background()) })
	scan, err := inst.Lane(LaneScan).OpenTab(ctx30(t), "run-focus")
	ok(t, err)
	apply, err := inst.Lane(LaneApply).OpenTab(ctx30(t), "run-focus")
	ok(t, err)
	_, err = inst.Lane(LaneApply).OpenTab(ctx30(t), "run-focus")
	ok(t, err)
	fx := newFixtures()
	t.Cleanup(fx.close)
	navigate(t, inst, LaneScan, scan, fx.Loopback("/probe"))
	navigate(t, inst, LaneApply, apply, fx.Loopback("/probe"))
	time.Sleep(500 * time.Millisecond)
	if after := frontApp(); after != before {
		t.Fatalf("foreground application changed from %q to %q", before, after)
	}
}
