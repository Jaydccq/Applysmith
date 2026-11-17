package browser

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/target"
)

// adoptRig is a surviving "Chrome" for the adoption tests: a shim process that
// carries the recorded profile and port on its command line, and a loopback
// server that answers /json/version for that port. The process listing and
// the port-owner lookup go through seams, so no real Chrome or lsof is needed.
type adoptRig struct {
	state, profile string
	proc           *Process
	port           int
	browser        string // the Browser field of /json/version
	rec            string
	t              *testing.T
	notes          []string
	hangWS         bool // the WebSocket endpoint accepts and never answers
}

func newAdoptRig(t *testing.T) *adoptRig {
	t.Helper()
	r := &adoptRig{t: t, state: t.TempDir(), browser: "Chrome/155.0.8059.40"}
	profile := filepath.Join(t.TempDir(), "profile")
	canon, err := canonical(profile)
	if err != nil {
		t.Fatal(err)
	}
	r.profile = canon
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r.port = l.Addr().(*net.TCPAddr).Port
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if r.hangWS && strings.HasPrefix(req.URL.Path, "/devtools/") {
			<-req.Context().Done()
			return
		}
		if r.browser == "" {
			_, _ = w.Write([]byte("not json"))
			return
		}
		_, _ = fmt.Fprintf(w, `{"Browser":%q,"webSocketDebuggerUrl":"ws://127.0.0.1:%d/devtools/browser/x"}`, r.browser, r.port)
	}))
	srv.Listener = l
	srv.Start()
	t.Cleanup(srv.Close)
	r.proc, err = spawn(LaunchOptions{Chrome: Chrome{Path: shim(t, `while :; do sleep 1; done`)}, Environ: os.Environ()}, canon, r.port)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { killGroup(r.proc.PID) })
	r.rec = filepath.Join(r.state, "instance.json")
	if err := WriteRecord(r.rec, Record{PID: r.proc.PID, Start: r.proc.Start, Profile: canon, Port: r.port, ChromeVersion: "155.0.8059.40"}); err != nil {
		t.Fatal(err)
	}
	r.owners(r.proc.PID, nil)
	return r
}

// owners makes the port-owner lookup report pid (or fail).
func (r *adoptRig) owners(pid int, err error) {
	old := listPortOwners
	listPortOwners = func(context.Context, int) ([]listener, error) {
		if err != nil {
			return nil, err
		}
		return []listener{{PID: pid, Addr: fmt.Sprintf("127.0.0.1:%d", r.port)}}, nil
	}
	r.t.Cleanup(func() { listPortOwners = old })
}

func (r *adoptRig) open(t *testing.T) (*Process, error) {
	t.Helper()
	return startOrAdopt(context.Background(), Options{
		Chrome: Chrome{Path: shim(t, `exit 1`), Major: 155}, ProfileDir: r.profile, StateDir: r.state, Environ: os.Environ(),
	}, func(kind, detail string) { r.notes = append(r.notes, kind+": "+detail) })
}

func (r *adoptRig) recordKept() bool {
	_, err := os.Stat(r.rec)
	return err == nil
}

func waitGone(t *testing.T, pid int) bool {
	t.Helper()
	for range 100 {
		if !processAlive(pid) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func TestBrowser_Adopt_HonestRecordIsAdoptedAndKept(t *testing.T) {
	r := newAdoptRig(t)
	p, err := r.open(t)
	if err != nil {
		t.Fatal(err)
	}
	defer p.stopWatching()
	if p.PID != r.proc.PID || !r.recordKept() {
		t.Fatalf("adopted pid %d (want %d), record kept %v", p.PID, r.proc.PID, r.recordKept())
	}
}

func TestBrowser_Adopt_TransientFailuresKeepTheRecordAndTheBrowser(t *testing.T) {
	for name, arrange := range map[string]func(*adoptRig){
		"port-owner tool fails":    func(r *adoptRig) { r.owners(0, errors.New("lsof: signal: killed")) },
		"version request not json": func(r *adoptRig) { r.browser = "" },
		"process start tool fails": func(r *adoptRig) {
			old := startOf
			startOf = func(context.Context, int) (string, error) { return "", errors.New("ps: exit status 1") }
			r.t.Cleanup(func() { startOf = old })
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newAdoptRig(t)
			arrange(r)
			_, err := r.open(t)
			if code, _ := CodeOf(err); code != CodeBrowserDisconnected {
				t.Fatalf("code %q err %v, want browser-disconnected", code, err)
			}
			if !r.recordKept() || !processAlive(r.proc.PID) {
				t.Fatalf("record kept %v, browser alive %v: a failed look must change nothing", r.recordKept(), processAlive(r.proc.PID))
			}
		})
	}
}

func TestBrowser_Adopt_OurBrowserOfAnotherMajorIsStoppedAndRelaunched(t *testing.T) {
	r := newAdoptRig(t)
	r.browser = "Chrome/154.0.1.1"
	_, err := r.open(t)
	if code, _ := CodeOf(err); code != CodeLaunchFailed {
		t.Fatalf("code %q err %v, want a fresh launch attempt", code, err)
	}
	if !waitGone(t, r.proc.PID) {
		t.Fatal("the old-major browser was left running and would hold the profile")
	}
	if len(r.notes) != 1 || !strings.HasPrefix(r.notes[0], "browser_retired: ") || !strings.Contains(r.notes[0], "major version 155") {
		t.Fatalf("the retirement left no event with its reason: %v", r.notes)
	}
	if r.recordKept() {
		t.Fatal("the record outlived the browser it described")
	}
}

func TestBrowser_Adopt_OurBrowserBehindAForeignListenerIsStoppedAndRelaunched(t *testing.T) {
	r := newAdoptRig(t)
	r.owners(os.Getpid(), nil)
	_, err := r.open(t)
	if code, _ := CodeOf(err); code != CodeLaunchFailed {
		t.Fatalf("code %q err %v, want a fresh launch attempt", code, err)
	}
	if !waitGone(t, r.proc.PID) || r.recordKept() {
		t.Fatalf("browser gone %v, record kept %v", !processAlive(r.proc.PID), r.recordKept())
	}
}

func TestBrowser_Adopt_RecycledPidIsRemovedAndNeverSignalled(t *testing.T) {
	r := newAdoptRig(t)
	bystander := exec.Command("sleep", "60")
	detach(bystander)
	if err := bystander.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bystander.Process.Kill(); _ = bystander.Wait() })
	if err := WriteRecord(r.rec, Record{PID: bystander.Process.Pid, Start: "Thu Jan  1 00:00:00 1970", Profile: r.profile, Port: r.port}); err != nil {
		t.Fatal(err)
	}
	_, err := r.open(t)
	if code, _ := CodeOf(err); code != CodeLaunchFailed {
		t.Fatalf("code %q err %v, want a fresh launch attempt", code, err)
	}
	if r.recordKept() || !processAlive(bystander.Process.Pid) {
		t.Fatalf("record kept %v, bystander alive %v", r.recordKept(), processAlive(bystander.Process.Pid))
	}
}

func TestBrowser_AdoptWatcher_EndsOnPidReuseAndOnRelease(t *testing.T) {
	old := watchInterval
	watchInterval = 5 * time.Millisecond
	t.Cleanup(func() { watchInterval = old })
	bystander := sleeper(t)
	start, err := processStart(context.Background(), bystander.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}

	reused := &Process{PID: bystander.Process.Pid, Start: "Thu Jan  1 00:00:00 1970", done: make(chan struct{})}
	go reused.watch(context.Background())
	select {
	case <-reused.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the watcher kept polling a pid that now belongs to another process")
	}

	same := &Process{PID: bystander.Process.Pid, Start: start, done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan struct{})
	go func() { same.watch(ctx); close(returned) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("the watcher outlived its release")
	}
	select {
	case <-same.done:
		t.Fatal("a released watcher reported an exit")
	default:
	}
}

func TestBrowser_OpenKey_SpellingsOfOneDirectoryShareALaunch(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	want := profileKey(filepath.Join(real, "profile"))
	for _, spelling := range []string{
		filepath.Join(link, "profile"),
		filepath.Join(real, "x", "..", "profile"),
		filepath.Join(link, ".", "profile"),
	} {
		if got := profileKey(spelling); got != want {
			t.Errorf("profileKey(%q) = %q, want %q", spelling, got, want)
		}
	}
}

func TestBrowser_Adopt_ABrowserThatWillNotQuitKeepsItsRecord(t *testing.T) {
	oldStop, oldKill := stopGrace, killGrace
	stopGrace, killGrace = 200*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { stopGrace, killGrace = oldStop, oldKill })
	r := newAdoptRig(t)
	// A shim that ignores SIGTERM, whose recorded start time the identity seam
	// vouches for while the real process listing disagrees, so the force kill's
	// own recycled-pid check refuses and the process stays.
	r.proc.stopWatching()
	killGroup(r.proc.PID)
	ready := filepath.Join(t.TempDir(), "ready")
	bin := shim(t, `trap "" TERM; touch "`+ready+`"; while :; do sleep 1; done`)
	p, err := spawn(LaunchOptions{Chrome: Chrome{Path: bin}, Environ: os.Environ()}, r.profile, r.port)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { killGroup(p.PID) })
	for range 200 {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := WriteRecord(r.rec, Record{PID: p.PID, Start: "vouched", Profile: r.profile, Port: r.port}); err != nil {
		t.Fatal(err)
	}
	r.owners(p.PID, nil)
	old := startOf
	startOf = func(context.Context, int) (string, error) { return "vouched", nil }
	t.Cleanup(func() { startOf = old })
	r.browser = "Chrome/154.0.1.1"
	_, err = r.open(t)
	if code, _ := CodeOf(err); code != CodeProfileInUse {
		t.Fatalf("code %q err %v, want profile-in-use", code, err)
	}
	if !r.recordKept() || !processAlive(p.PID) {
		t.Fatalf("record kept %v, browser alive %v: the record is the only trace of a browser still holding the profile", r.recordKept(), processAlive(p.PID))
	}
}

func TestBrowser_TabsOfABrowserThatExitedOnItsOwnAreClosed(t *testing.T) {
	root, cancel := context.WithCancel(context.Background())
	defer cancel()
	i := &Instance{root: root, tabs: map[target.ID]*tab{}, windows: map[int64]Lane{}}
	closed := make(chan struct{}, 2)
	for _, id := range []target.ID{"a", "b"} {
		tb := &tab{id: id}
		tb.onClosed = []func(){func() { closed <- struct{}{} }}
		i.tabs[id] = tb
	}
	exited := make(chan struct{})
	go i.closeTabsWhenGone(exited)
	close(exited)
	for range 2 {
		select {
		case <-closed:
		case <-time.After(2 * time.Second):
			t.Fatal("a tab of the exited browser never fired its close callback")
		}
	}
}

func TestBrowser_TabsAreLeftAloneWhenTheInstanceEndsBeforeTheBrowser(t *testing.T) {
	root, cancel := context.WithCancel(context.Background())
	i := &Instance{root: root, tabs: map[target.ID]*tab{}, windows: map[int64]Lane{}}
	fired := make(chan struct{}, 1)
	i.tabs["a"] = &tab{id: "a", onClosed: []func(){func() { fired <- struct{}{} }}}
	returned := make(chan struct{})
	go func() { i.closeTabsWhenGone(make(chan struct{})); close(returned) }()
	cancel()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("the watcher outlived the instance")
	}
	select {
	case <-fired:
		t.Fatal("a detached instance closed its tabs")
	default:
	}
}

func TestBrowser_Adopt_HungAttachFailsAtBudget(t *testing.T) {
	old := attachBudget
	attachBudget = 300 * time.Millisecond
	t.Cleanup(func() { attachBudget = old })
	r := newAdoptRig(t)
	r.hangWS = true
	o := Options{Chrome: Chrome{Path: shim(t, `exit 1`), Major: 155}, ProfileDir: r.profile, StateDir: r.state, Environ: os.Environ()}
	start := time.Now()
	_, err := openOnce(context.Background(), o)
	if code, _ := CodeOf(err); code != CodeBrowserDisconnected {
		t.Fatalf("code %q err %v, want browser-disconnected", code, err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("the attach took %v; the budget is %v", d, attachBudget)
	}
	if msg := err.Error(); strings.Contains(msg, "ws://") || strings.Contains(msg, "/devtools/") || strings.Contains(msg, fmt.Sprint(r.port)) {
		t.Fatalf("the error names the endpoint: %s", msg)
	}
}

// lockedBy makes pid the profile's SingletonLock holder, with no record.
func (r *adoptRig) lockedBy(pid int) {
	_ = os.Remove(r.rec)
	_ = os.MkdirAll(r.profile, 0o700)
	if err := os.Symlink(fmt.Sprintf("example-host-%d", pid), filepath.Join(r.profile, "SingletonLock")); err != nil {
		r.t.Fatal(err)
	}
}

func TestBrowser_Adopt_OurLockHolderWithoutARecordIsAdopted(t *testing.T) {
	r := newAdoptRig(t)
	r.lockedBy(r.proc.PID)
	p, err := r.open(t)
	if err != nil {
		t.Fatal(err)
	}
	defer p.stopWatching()
	if p.PID != r.proc.PID || p.Version != "155.0.8059.40" || len(r.notes) != 1 || !strings.Contains(r.notes[0], "found by the profile lock") {
		t.Fatalf("adopted pid %d version %q (want %d), notes %v", p.PID, p.Version, r.proc.PID, r.notes)
	}
}

func TestBrowser_Adopt_ALockHolderThatIsNotOursIsNeverTouched(t *testing.T) {
	for name, arrange := range map[string]func(*adoptRig) int{
		"another profile": func(r *adoptRig) int {
			p, err := spawn(LaunchOptions{Chrome: Chrome{Path: shim(r.t, `while :; do sleep 1; done`)}, Environ: os.Environ()}, r.profile+"2", r.port)
			if err != nil {
				r.t.Fatal(err)
			}
			r.t.Cleanup(func() { killGroup(p.PID) })
			r.owners(p.PID, nil)
			r.lockedBy(p.PID)
			return p.PID
		},
		"not a browser": func(r *adoptRig) int {
			c := exec.Command("sleep", "60")
			detach(c)
			if err := c.Start(); err != nil {
				r.t.Fatal(err)
			}
			r.t.Cleanup(func() { _ = c.Process.Kill(); _ = c.Wait() })
			r.owners(c.Process.Pid, nil)
			r.lockedBy(c.Process.Pid)
			return c.Process.Pid
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newAdoptRig(t)
			pid := arrange(r)
			_, err := r.open(t)
			if code, _ := CodeOf(err); code != CodeProfileInUse || len(r.notes) != 0 {
				t.Fatalf("code %q err %v notes %v, want profile-in-use and no adoption or retirement", code, err, r.notes)
			}
			time.Sleep(300 * time.Millisecond)
			if !processAlive(pid) {
				t.Fatal("a process that is not verifiably ours was stopped")
			}
		})
	}
}

func TestBrowser_Control_SurvivesWhileARecordOrTheProfileLockNamesALiveProcess(t *testing.T) {
	dir := t.TempDir()
	state, profile := filepath.Join(dir, "state"), filepath.Join(dir, "profile")
	for _, d := range []string{state, profile} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	c := NewControl(Options{StateDir: state, ProfileDir: profile})
	if c.Survives() {
		t.Fatal("a browser survives with no record and no lock")
	}
	ended := exec.Command("true")
	if err := ended.Run(); err != nil {
		t.Fatal(err)
	}
	gone, live := ended.Process.Pid, os.Getpid()
	rec, lock := filepath.Join(state, "instance.json"), filepath.Join(profile, "SingletonLock")
	for _, c2 := range []struct {
		pid  int
		want bool
	}{{gone, false}, {live, true}} {
		if err := WriteRecord(rec, Record{PID: c2.pid, Port: 9222, Profile: profile}); err != nil {
			t.Fatal(err)
		}
		if c.Survives() != c2.want {
			t.Errorf("record of pid %d: survives %v", c2.pid, !c2.want)
		}
	}
	if err := os.Remove(rec); err != nil {
		t.Fatal(err)
	}
	for _, c2 := range []struct {
		pid  int
		want bool
	}{{gone, false}, {live, true}} {
		_ = os.Remove(lock)
		if err := os.Symlink(fmt.Sprintf("host.local-%d", c2.pid), lock); err != nil {
			t.Fatal(err)
		}
		if c.Survives() != c2.want {
			t.Errorf("lock of pid %d: survives %v", c2.pid, !c2.want)
		}
	}
}
