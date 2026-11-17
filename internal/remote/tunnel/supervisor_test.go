package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	switch {
	case len(os.Args) > 1 && os.Args[1] == "tunnel": // started as the tunnel executable
		os.Stdout.WriteString("INF Registered tunnel connection connIndex=0\n")
		waitForTerm()
		os.Exit(0)
	case os.Getenv("TUNNEL_TEST_CHILD") == "sleep":
		waitForTerm()
		os.Exit(0)
	case filepath.Base(os.Args[0]) == "cloudflared":
		os.Exit(2) // a stand-in executable never runs the tests, which would start more stand-ins
	}
	os.Exit(m.Run())
}

func waitForTerm() {
	ch := make(chan os.Signal, 1)
	signalNotify(ch)
	<-ch
}

// fakeClock fires After channels when the test advances time.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []*waiter
	delays  []time.Duration
}

type waiter struct {
	at time.Time
	d  time.Duration
	ch chan time.Time
}

func newClock() *fakeClock { return &fakeClock{now: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.delays = append(c.delays, d)
	w := &waiter{at: c.now.Add(d), d: d, ch: make(chan time.Time, 1)}
	c.waiters = append(c.waiters, w)
	return w.ch
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	kept := c.waiters[:0]
	for _, w := range c.waiters {
		if !w.at.After(c.now) {
			w.ch <- c.now
		} else {
			kept = append(kept, w)
		}
	}
	c.waiters = kept
}

// restartDelays are the After calls that wait out a restart, not heartbeats.
func (c *fakeClock) restartDelays() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []time.Duration
	for _, d := range c.delays {
		if d != heartbeatEvery && d != stopGrace {
			out = append(out, d)
		}
	}
	return out
}

// waiting reports whether an After(d) call is waiting to fire.
func (c *fakeClock) waiting(d time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.ContainsFunc(c.waiters, func(w *waiter) bool { return w.d == d })
}

// fakeChild is a scripted child process.
type fakeChild struct {
	pid                int
	out                *io.PipeWriter
	outR               *io.PipeReader
	exit               chan struct{}
	once               sync.Once
	exitOnTerm         bool
	mu                 sync.Mutex
	terminated, killed bool
}

func newChild(pid int, exitOnTerm bool) *fakeChild {
	r, w := io.Pipe()
	return &fakeChild{pid: pid, out: w, outR: r, exit: make(chan struct{}), exitOnTerm: exitOnTerm}
}

func (c *fakeChild) PID() int          { return c.pid }
func (c *fakeChild) Output() io.Reader { return c.outR }
func (c *fakeChild) Wait() error       { <-c.exit; return errors.New("exit status 1") }
func (c *fakeChild) finish()           { c.once.Do(func() { _ = c.out.Close(); close(c.exit) }) }
func (c *fakeChild) say(line string)   { _, _ = io.WriteString(c.out, line+"\n") }
func (c *fakeChild) Terminate() error {
	c.mu.Lock()
	c.terminated = true
	c.mu.Unlock()
	if c.exitOnTerm {
		c.finish()
	}
	return nil
}
func (c *fakeChild) Kill() error {
	c.mu.Lock()
	c.killed = true
	c.mu.Unlock()
	c.finish()
	return nil
}
func (c *fakeChild) flags() (term, kill bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.terminated, c.killed
}

// rig is a supervisor over a fake clock and scripted children.
type rig struct {
	t      *testing.T
	clock  *fakeClock
	sup    *Supervisor
	mu     sync.Mutex
	specs  []Spec
	script []func(*fakeChild)
	kids   []*fakeChild
	writes int
	last   Status
}

func newRig(t *testing.T, cfg Config, exitOnTerm bool, script ...func(*fakeChild)) *rig {
	t.Helper()
	r := &rig{t: t, clock: newClock(), script: script}
	if cfg.Token == nil {
		cfg.Token = func() (string, error) { return "tok-EXAMPLE-fixture", nil }
		if cfg.PublicURL == "" {
			cfg.PublicURL = "https://applysmith.example.com"
		}
	}
	cfg.Now, cfg.After = r.clock.Now, r.clock.After
	if cfg.Finder.Version == nil {
		cfg.Finder = Finder{LookPath: func(string) (string, error) { return "/opt/example/cloudflared", nil },
			Version: func(context.Context, string) (string, error) { return "2026.1.0", nil }}
	}
	cfg.Write = func(_ string, st Status) error { r.mu.Lock(); r.writes++; r.last = st; r.mu.Unlock(); return nil }
	cfg.Environ = func() []string {
		return []string{"PATH=/usr/bin", "APPLYSMITH_SECRET_API_TOKEN=do-not-pass", "HOME=/home/alex"}
	}
	cfg.Starter = func(spec Spec) (Child, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.specs = append(r.specs, spec)
		k := newChild(1000+len(r.kids), exitOnTerm)
		r.kids = append(r.kids, k)
		i := len(r.kids) - 1
		if i < len(r.script) && r.script[i] != nil {
			go r.script[i](k)
		} else if len(r.script) > 0 && r.script[len(r.script)-1] != nil && i >= len(r.script) {
			go r.script[len(r.script)-1](k)
		}
		return k, nil
	}
	r.sup = New(cfg)
	t.Cleanup(r.sup.Stop)
	return r
}

func (r *rig) child(i int) *fakeChild {
	r.t.Helper()
	var k *fakeChild
	waitFor(r.t, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		if i < len(r.kids) {
			k = r.kids[i]
		}
		return k != nil
	})
	return k
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached in time")
		}
		time.Sleep(time.Millisecond)
	}
}

func (r *rig) waitMode(m State) Status {
	r.t.Helper()
	var st Status
	waitFor(r.t, func() bool { st = r.sup.Status(); return st.Mode == m })
	return st
}

func exitNow(c *fakeChild) { c.finish() }

func TestTunnel_NamedModeArguments(t *testing.T) {
	args, env := Command("tok-example")
	if got := strings.Join(args, " "); got != "tunnel --no-autoupdate run" || len(env) != 1 || env[0] != "TUNNEL_TOKEN=tok-example" {
		t.Errorf("named: %q %v", got, env)
	}
}

func TestTunnel_NamedTokenInEnvironmentNeverArgs(t *testing.T) {
	const token = "tok-example-not-a-secret"
	r := newRig(t, Config{PublicURL: "https://applysmith.example.com", Token: func() (string, error) { return token, nil }}, true)
	r.sup.Start()
	k := r.child(0)
	r.mu.Lock()
	spec := r.specs[0]
	r.mu.Unlock()
	for _, a := range spec.Args {
		if strings.Contains(a, token) {
			t.Errorf("the token is in the argument list: %v", spec.Args)
		}
	}
	if !slices.Contains(spec.Env, "TUNNEL_TOKEN="+token) {
		t.Errorf("TUNNEL_TOKEN is not in the environment: %v", spec.Env)
	}
	for _, kv := range spec.Env {
		if strings.HasPrefix(kv, "APPLYSMITH_SECRET_") {
			t.Errorf("a secret of this program reached the child: %s", kv)
		}
	}
	k.say("INF Registered tunnel connection connIndex=0")
	st := r.waitMode(StateRunning)
	if st.PublicURL != "https://applysmith.example.com" {
		t.Errorf("public URL %q", st.PublicURL)
	}
}

func TestTunnel_BinaryMissingIsErrorStateNoDownload(t *testing.T) {
	cfg := Config{Finder: Finder{LookPath: func(string) (string, error) { return "", errors.New("not found") }, Candidates: []string{}}}
	r := newRig(t, cfg, true)
	cfg.Finder.Version = func(context.Context, string) (string, error) { return "", errors.New("unused") }
	r.sup = New(Config{Finder: cfg.Finder, Now: r.clock.Now, After: r.clock.After, Write: func(string, Status) error { return nil },
		Starter: func(Spec) (Child, error) {
			t.Error("a child was started without an executable")
			return nil, errors.New("no")
		}})
	t.Cleanup(r.sup.Stop)
	r.sup.Start()
	st := r.waitMode(StateError)
	if st.Reason != ReasonNotFound || !strings.Contains(st.Error, "install") {
		t.Errorf("status %+v", st)
	}
}

func TestTunnel_NamedNeedsValidPublicURL(t *testing.T) {
	for _, public := range []string{"", "http://applysmith.example.com", "https://fresh-example.trycloudflare.com", "https://user@applysmith.example.com"} {
		t.Run(public, func(t *testing.T) {
			r := newRig(t, Config{PublicURL: public, Token: func() (string, error) { return "tok-example", nil }}, true)
			r.sup.Start()
			if st := r.waitMode(StateError); st.Reason != ReasonNeedsURL {
				t.Fatalf("invalid named public URL: %+v", st)
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			if len(r.specs) != 0 {
				t.Fatal("a child was started with invalid named-tunnel configuration")
			}
		})
	}
}

func backoffSequence(t *testing.T) {
	r := newRig(t, Config{}, true, exitNow)
	r.sup.Start()
	want := []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second}
	// The last delay is not advanced, so no further restart is recorded.
	for i := 1; ; i++ {
		waitFor(t, func() bool { return len(r.clock.restartDelays()) == i })
		if i == len(want) {
			break
		}
		r.waitMode(StateError)
		r.clock.Advance(r.clock.restartDelays()[i-1])
	}
	if got := r.clock.restartDelays(); !slices.Equal(got, want) {
		t.Errorf("restart delays %v, want %v", got, want)
	}
}

func backoffResetsAfterAStableRun(t *testing.T) {
	hold := func(c *fakeChild) {}
	r := newRig(t, Config{}, true, exitNow, hold)
	r.sup.Start()
	waitFor(t, func() bool { return len(r.clock.restartDelays()) == 1 })
	r.clock.Advance(time.Second)
	second := r.child(1)
	r.clock.Advance(61 * time.Second)
	second.finish()
	waitFor(t, func() bool { return len(r.clock.restartDelays()) == 2 })
	if got := r.clock.restartDelays(); got[1] != time.Second {
		t.Errorf("the delay after a 61 second run is %v, want 1s", got[1])
	}
}

func TestTunnel_BackoffSequenceResetAndFailedAfterTwenty(t *testing.T) {
	t.Run("sequence", backoffSequence)
	t.Run("reset after a stable run", backoffResetsAfterAStableRun)
	t.Run("failed after twenty", budgetExhaustedFailsUntilRetry)
}

func budgetExhaustedFailsUntilRetry(t *testing.T) {
	r := newRig(t, Config{}, true, exitNow)
	r.sup.Start()
	for i := 1; i < maxFailures; i++ {
		waitFor(t, func() bool { return len(r.clock.restartDelays()) == i })
		r.clock.Advance(r.clock.restartDelays()[i-1])
	}
	st := r.waitMode(StateFailed)
	if st.ConsecutiveFailures != maxFailures || st.Reason != ReasonRetries {
		t.Errorf("status %+v", st)
	}
	time.Sleep(20 * time.Millisecond)
	if n := len(r.clock.restartDelays()); n != maxFailures-1 {
		t.Errorf("%d restart delays; no automatic restart may follow the failed state", n)
	}
	r.mu.Lock()
	before := len(r.specs)
	r.mu.Unlock()
	r.sup.Retry()
	waitFor(t, func() bool { r.mu.Lock(); defer r.mu.Unlock(); return len(r.specs) > before })
}

func TestTunnel_StopTerminatesThenKillsAfterFiveSeconds(t *testing.T) {
	stubborn := newRig(t, Config{}, false)
	stubborn.sup.Start()
	k := stubborn.child(0)
	k.say("INF Registered tunnel connection connIndex=0")
	stubborn.waitMode(StateRunning)
	done := make(chan struct{})
	go func() { stubborn.sup.Stop(); close(done) }()
	waitFor(t, func() bool { term, _ := k.flags(); return term })
	if _, killed := k.flags(); killed {
		t.Fatal("killed before the grace period")
	}
	waitFor(t, func() bool { return stubborn.clock.waiting(stopGrace) })
	stubborn.clock.Advance(stopGrace)
	<-done
	if _, killed := k.flags(); !killed {
		t.Error("a child that ignores terminate was not killed after 5 seconds")
	}
	if st := stubborn.sup.Status(); st.Mode != StateStopped || st.PID != 0 {
		t.Errorf("after stop: %+v", st)
	}

	polite := newRig(t, Config{}, true)
	polite.sup.Start()
	p := polite.child(0)
	polite.sup.Stop()
	if term, killed := p.flags(); !term || killed {
		t.Errorf("polite child: terminated=%v killed=%v", term, killed)
	}
}

// A line longer than the scanner holds ends the reading of lines, not the
// reading: a child whose output nobody reads blocks on its next write and its
// exit is never seen, so Stop would wait for ever.
func TestTunnel_AnOverlongOutputLineDoesNotBlockTheChild(t *testing.T) {
	r := newRig(t, Config{}, true)
	r.sup.Start()
	k := r.child(0)
	wrote := make(chan struct{})
	go func() {
		k.say(strings.Repeat("x", 2<<20))
		k.say("INF still writing")
		close(wrote)
	}()
	waitFor(t, func() bool {
		select {
		case <-wrote:
			return true
		default:
			return false
		}
	})
	r.sup.Stop()
}

func TestTunnel_HeartbeatEveryTenSeconds(t *testing.T) {
	r := newRig(t, Config{}, true)
	r.sup.Start()
	r.child(0).say("INF Registered tunnel connection connIndex=0")
	// The running state's own write has landed, so only heartbeats follow.
	waitFor(t, func() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.last.Mode == StateRunning })
	r.mu.Lock()
	before := r.writes
	r.mu.Unlock()
	for beat := 1; beat <= 2; beat++ {
		waitFor(t, func() bool { return r.clock.waiting(heartbeatEvery) })
		r.clock.Advance(heartbeatEvery)
		waitFor(t, func() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.writes-before >= beat })
	}
}

// Status writes land in the order of the states they hold: a heartbeat that
// was already writing when the state changed does not leave its older state
// in the file.
func TestTunnel_AHeartbeatInFlightDoesNotOverwriteANewerState(t *testing.T) {
	var mu sync.Mutex
	var file []State
	entered, hold := make(chan struct{}), make(chan struct{})
	first := true
	fire := make(chan time.Time, 1)
	fire <- time.Time{}
	s := New(Config{
		After: func(time.Duration) <-chan time.Time { return fire },
		Write: func(_ string, st Status) error {
			mu.Lock()
			held := first
			first = false
			mu.Unlock()
			if held {
				close(entered)
				<-hold
			}
			mu.Lock()
			file = append(file, st.Mode)
			mu.Unlock()
			return nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.heartbeat(ctx)
	<-entered
	changed := make(chan struct{})
	go func() {
		defer close(changed)
		s.update(func(st *Status) { st.Mode = StateRunning })
	}()
	select {
	case <-changed:
	case <-time.After(200 * time.Millisecond):
	}
	close(hold)
	<-changed
	mu.Lock()
	defer mu.Unlock()
	if len(file) != 2 || file[1] != StateRunning {
		t.Fatalf("writes landed as %v; the file must end on the newer state", file)
	}
}

func TestTunnel_StatusFileIsAtomicAndHoldsNoSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "tunnel.json")
	stop := make(chan struct{})
	first := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			st := Status{Mode: StateRunning, PublicURL: "https://x" + strings.Repeat("a", i%200) + ".trycloudflare.com", PID: i}
			if err := WriteFile(path, st); err != nil {
				t.Error(err)
				return
			}
			if i == 0 {
				close(first)
			}
		}
	}()
	<-first
	for range 300 {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if st := ReadFile(path, time.Now()); st.Mode != StateRunning && len(data) > 0 {
			t.Fatalf("a reader saw a partial document: %q", data)
		}
	}
	close(stop)
	wg.Wait()
	data, _ := os.ReadFile(path)
	if strings.Contains(strings.ToLower(string(data)), "token") {
		t.Errorf("document mentions a token: %s", data)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("temporary files were left behind: %v", entries)
	}
}

func TestTunnel_StateFileStaleAfterThirtySeconds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tunnel.json")
	if err := WriteFile(path, Status{Mode: StateRunning, PublicURL: "https://x.trycloudflare.com"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if ReadFile(path, now).Stale {
		t.Error("a fresh file is stale")
	}
	old := now.Add(-45 * time.Second)
	_ = os.Chtimes(path, old, old)
	if !ReadFile(path, now).Stale {
		t.Error("a 45 second old running file is not stale")
	}
	if err := WriteFile(path, Status{Mode: StateStopped}); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(path, old, old)
	if ReadFile(path, now).Stale {
		t.Error("an old stopped file is reported stale")
	}
	if st := ReadFile(filepath.Join(t.TempDir(), "absent.json"), now); st.Mode != StateOff {
		t.Errorf("absent file: %+v", st)
	}
}

type fakeProcs struct {
	alive              map[int]bool
	comm               map[int]string
	started            map[int]time.Time
	dieOnTerm          bool
	terminated, killed []int
}

func (f *fakeProcs) StartTime(pid int) (time.Time, bool) {
	t, ok := f.started[pid]
	return t, ok
}
func (f *fakeProcs) Alive(pid int) bool     { return f.alive[pid] }
func (f *fakeProcs) Command(pid int) string { return f.comm[pid] }
func (f *fakeProcs) Terminate(pid int) error {
	f.terminated = append(f.terminated, pid)
	if f.dieOnTerm {
		f.alive[pid] = false
	}
	return nil
}
func (f *fakeProcs) Kill(pid int) error {
	f.killed = append(f.killed, pid)
	f.alive[pid] = false
	return nil
}

func TestTunnel_OrphanCleanup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tunnel.json")
	began := time.Date(2026, 10, 12, 9, 0, 0, 0, time.Local)
	write := func(pid int) {
		if err := WriteFile(path, Status{Mode: StateRunning, PID: pid, StartedAt: &began}); err != nil {
			t.Fatal(err)
		}
	}
	noSleep := func(time.Duration) {}
	started := map[int]time.Time{4242: began.Add(-time.Second)}

	write(4242)
	p := &fakeProcs{alive: map[int]bool{4242: true}, comm: map[int]string{4242: "/opt/homebrew/bin/cloudflared"}, started: started, dieOnTerm: true}
	if !CleanOrphan(path, p, noSleep) || !slices.Equal(p.terminated, []int{4242}) || len(p.killed) != 0 {
		t.Errorf("a live cloudflared: terminated %v killed %v", p.terminated, p.killed)
	}

	p = &fakeProcs{alive: map[int]bool{4242: true}, comm: map[int]string{4242: "/usr/bin/cloudflared"}, started: started}
	CleanOrphan(path, p, noSleep)
	if !slices.Equal(p.killed, []int{4242}) {
		t.Errorf("a process that ignores terminate must be killed: %v", p.killed)
	}

	p = &fakeProcs{alive: map[int]bool{4242: true}, comm: map[int]string{4242: "/usr/bin/vim"}, started: started}
	if CleanOrphan(path, p, noSleep) || len(p.terminated)+len(p.killed) != 0 {
		t.Error("a process that is not cloudflared was signalled")
	}
	p = &fakeProcs{alive: map[int]bool{}, comm: map[int]string{4242: "cloudflared"}, started: started}
	if CleanOrphan(path, p, noSleep) || len(p.terminated) != 0 {
		t.Error("a dead process was signalled")
	}
}

// After a reboot or a long time the system may give the recorded identifier to
// another cloudflared, one the user runs for another purpose. It did not start
// when the status file says the child did, so it is left alone.
func TestTunnel_OrphanCleanupSparesAReusedIdentifier(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tunnel.json")
	began := time.Date(2026, 10, 12, 9, 0, 0, 0, time.Local)
	if err := WriteFile(path, Status{Mode: StateRunning, PID: 4242, StartedAt: &began}); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]*fakeProcs{
		"started days later":      {alive: map[int]bool{4242: true}, comm: map[int]string{4242: "/usr/local/bin/cloudflared"}, started: map[int]time.Time{4242: began.Add(72 * time.Hour)}},
		"started long before":     {alive: map[int]bool{4242: true}, comm: map[int]string{4242: "/usr/local/bin/cloudflared"}, started: map[int]time.Time{4242: began.Add(-time.Hour)}},
		"start time not readable": {alive: map[int]bool{4242: true}, comm: map[int]string{4242: "/usr/local/bin/cloudflared"}},
	} {
		if CleanOrphan(path, p, func(time.Duration) {}) || len(p.terminated)+len(p.killed) != 0 {
			t.Errorf("%s: an unrelated cloudflared was signalled", name)
		}
	}
	// A status file without a start time cannot vouch for any process.
	if err := WriteFile(path, Status{Mode: StateRunning, PID: 4242}); err != nil {
		t.Fatal(err)
	}
	p := &fakeProcs{alive: map[int]bool{4242: true}, comm: map[int]string{4242: "cloudflared"}, started: map[int]time.Time{4242: began}}
	if CleanOrphan(path, p, func(time.Duration) {}) || len(p.terminated) != 0 {
		t.Error("a process was signalled on a status file with no start time")
	}
}

func TestTunnel_FinderLocatesAndVerifies(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "cloudflared")
	script := "#!/bin/sh\necho 'cloudflared version 2026.2.3 (built 2026-02-01-0000 UTC)'\n"
	if err := os.WriteFile(good, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	f := Finder{LookPath: func(string) (string, error) { return "", errors.New("no") }, Candidates: []string{filepath.Join(dir, "missing"), good}}
	path, version, err := f.Locate(context.Background(), "auto")
	if err != nil || path != good || version != "2026.2.3" {
		t.Fatalf("auto: %q %q %v", path, version, err)
	}
	if path, version, err = f.Locate(context.Background(), good); err != nil || path != good || version != "2026.2.3" {
		t.Fatalf("explicit: %q %q %v", path, version, err)
	}
	if _, _, err = f.Locate(context.Background(), filepath.Join(dir, "nope")); !errors.Is(err, ErrNotFound) {
		t.Errorf("explicit path that does not exist: %v", err)
	}
	if _, _, err = (Finder{LookPath: f.LookPath, Candidates: []string{}}).Locate(context.Background(), "auto"); !errors.Is(err, ErrNotFound) {
		t.Errorf("nothing installed: %v", err)
	}
	bad := filepath.Join(dir, "broken")
	_ = os.WriteFile(bad, []byte("#!/bin/sh\nexit 3\n"), 0o700)
	if _, _, err = f.Locate(context.Background(), bad); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("a binary that fails its version command: %v", err)
	}
}

// helperCopy copies the test binary to a file named cloudflared, so a real
// process stands in for the tunnel executable.
func helperCopy(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Skip("cannot read the test binary")
	}
	dst := filepath.Join(t.TempDir(), "cloudflared")
	if err := os.WriteFile(dst, src, 0o700); err != nil {
		t.Fatal(err)
	}
	return dst
}

func TestTunnel_RealChildThroughTheExecStarter(t *testing.T) {
	bin := helperCopy(t)
	var log strings.Builder
	var mu sync.Mutex
	sup := New(Config{Binary: bin, PublicURL: "https://applysmith.example.com", Token: func() (string, error) { return "tok-EXAMPLE-fixture", nil }, StatusPath: filepath.Join(t.TempDir(), "tunnel.json"), Log: lockedWriter{&mu, &log},
		Finder:  Finder{Version: func(context.Context, string) (string, error) { return "test", nil }},
		Environ: os.Environ})
	t.Cleanup(sup.Stop)
	sup.Start()
	waitFor(t, func() bool { return sup.Status().Mode == StateRunning })
	st := sup.Status()
	if st.PublicURL != "https://applysmith.example.com" || st.PID <= 0 {
		t.Errorf("status %+v", st)
	}
	if proc, err := os.FindProcess(st.PID); err != nil || proc.Signal(syscall.Signal(0)) != nil {
		t.Errorf("child %d is not alive", st.PID)
	}
	sup.Stop()
	if proc, _ := os.FindProcess(st.PID); proc.Signal(syscall.Signal(0)) == nil {
		t.Errorf("child %d is still alive after stop", st.PID)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(log.String(), "Registered tunnel connection") {
		t.Errorf("the child's output did not reach the log: %q", log.String())
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func TestTunnel_RealOrphanIsTerminated(t *testing.T) {
	bin := helperCopy(t)
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "TUNNEL_TEST_CHILD=sleep")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	reaped := make(chan struct{})
	go func() { _ = cmd.Wait(); close(reaped) }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	path := filepath.Join(t.TempDir(), "tunnel.json")
	began := time.Now()
	if err := WriteFile(path, Status{Mode: StateRunning, PID: cmd.Process.Pid, StartedAt: &began}); err != nil {
		t.Fatal(err)
	}
	if !CleanOrphan(path, nil, nil) {
		t.Fatal("the orphan was not found")
	}
	select {
	case <-reaped:
	case <-time.After(5 * time.Second):
		t.Fatal("the orphan is still running")
	}
}

func TestTunnel_NoTokenRefusesUnsupportedQuickMode(t *testing.T) {
	r := newRig(t, Config{PublicURL: "https://applysmith.example.com", Token: func() (string, error) { return "", nil }}, true)
	r.sup.Start()
	st := r.waitMode(StateError)
	if st.Reason != "named_tunnel_required" || !strings.Contains(st.Error, "named") || !strings.Contains(st.Error, "SSE") {
		t.Fatalf("missing named configuration: %+v", st)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.specs) != 0 {
		t.Fatal("a child was started without a named-tunnel token")
	}
}

func TestTunnel_StatusDocumentFieldsNoSecret(t *testing.T) {
	const token = "tok-example-not-a-secret-value"
	r := newRig(t, Config{PublicURL: "https://applysmith.example.com", Token: func() (string, error) { return token, nil }}, true)
	r.sup.Start()
	r.child(0).say("INF Registered tunnel connection connIndex=0")
	r.waitMode(StateRunning)
	r.mu.Lock()
	last := r.last
	r.mu.Unlock()
	raw, err := json.Marshal(last)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	_ = json.Unmarshal(raw, &doc)
	var keys []string
	for k := range doc {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	want := []string{"consecutiveFailures", "error", "lastRestartAt", "mode", "pid", "publicUrl", "startedAt", "version"}
	if !slices.Equal(keys, want) {
		t.Errorf("document fields %v, want %v", keys, want)
	}
	if strings.Contains(string(raw), token) || doc["mode"] != "running" || doc["pid"] != float64(1000) || doc["startedAt"] == nil {
		t.Errorf("document %s", raw)
	}
}
