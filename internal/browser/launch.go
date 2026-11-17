package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"applysmith/internal/lazyregexp"
)

// Shutdown waits; variables so tests can shorten them.
var (
	stopGrace = 10 * time.Second
	killGrace = 5 * time.Second
)

const (
	launchDeadline  = 30 * time.Second
	maxLaunchTries  = 3 // the first attempt plus two bind-race retries
	endpointPoll    = 100 * time.Millisecond
	stderrKeep      = 8 << 10
	failureTextKeep = 400
)

// CodeLaunchFailed reports a Chrome child that exited before its endpoint
// answered; the message carries the start of its standard error.
const CodeLaunchFailed Code = "launch-failed"

// LaunchOptions configures one Chrome launch.
type LaunchOptions struct {
	Chrome     Chrome
	ProfileDir string
	// Environ is the caller's environment; the browser sees an allowlisted subset.
	Environ   []string
	hostRules string // hostnames to loopback (--host-resolver-rules); set by tests only
	headless  bool   // --headless=new; set by tests only, the product never runs headless

	pickPort func() (int, error) // test seam for the bind race
	timeout  time.Duration
}

// Process is a Chrome browser process whose DevTools endpoint has been
// verified as belonging to it.
type Process struct {
	PID     int
	Start   string
	Profile string
	Port    int
	Version string

	wsURL  string // in memory only, never logged
	done   chan struct{}
	stderr *capBuffer

	unwatch context.CancelFunc // ends the exit watcher of an adopted process

	// exit is how a child this server started ended. An adopted process is
	// not our child, so its status is unknown.
	exit atomic.Pointer[childExit]
}

// childExit is the wait status of a browser process this server started.
type childExit struct {
	clean  bool   // exit status 0: the browser quit on its own request
	status string // "exit status 0" or "signal: killed"
}

type capBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *capBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := stderrKeep - len(b.buf); room > 0 {
		b.buf = append(b.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

func (b *capBuffer) head(n int) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := wsRedact.ReplaceAllString(strings.TrimSpace(string(b.buf)), "ws://[redacted]")
	if len(s) > n {
		s = s[:n]
	}
	return s
}

// wsRedact matches a DevTools WebSocket address, which must not reach an error
// message: it names the browser endpoint.
var wsRedact = lazyregexp.New(`wss?://\S*`)

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// Launch starts Chrome on an explicit loopback port and returns once the
// endpoint is verified as owned by the child. A listener that is not the
// child (a lost port race) is never attached to; the child is terminated and
// the launch retried on another port, at most twice, inside one deadline.
func Launch(ctx context.Context, o LaunchOptions) (*Process, error) {
	if err := platformSupported(); err != nil {
		return nil, err
	}
	if o.timeout == 0 {
		o.timeout = launchDeadline
	}
	if o.pickPort == nil {
		o.pickPort = freePort
	}
	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()

	home, _ := os.UserHomeDir()
	getenv := environGetter(o.Environ)
	profile, err := prepareProfile(o.ProfileDir, home, getenv)
	if err != nil {
		return nil, err
	}
	if err := clearStaleSingleton(profile); err != nil {
		return nil, err
	}
	var lastErr error
	for try := 0; try < maxLaunchTries; try++ {
		port, err := o.pickPort()
		if err != nil {
			return nil, wrapError(CodeLaunchFailed, err, "no free loopback port")
		}
		p, err := spawn(o, profile, port)
		if err != nil {
			return nil, err
		}
		err = p.awaitEndpoint(ctx)
		if err == nil {
			return p, nil
		}
		p.terminate()
		lastErr = err
		if c, _ := CodeOf(err); c != CodeEndpointUnverified {
			return nil, err
		}
	}
	return nil, lastErr
}

func environGetter(environ []string) func(string) string {
	return func(k string) string {
		for _, kv := range environ {
			if name, v, ok := strings.Cut(kv, "="); ok && name == k {
				return v
			}
		}
		return ""
	}
}

func spawn(o LaunchOptions, profile string, port int) (*Process, error) {
	args := launchArgs(profile, port, o.hostRules)
	if f := forbiddenFlag(args); f != "" {
		return nil, newError(CodeLaunchFailed, "launch argument %s is forbidden", f)
	}
	if o.headless {
		args = append(args, "--headless=new")
	}
	cmd := exec.Command(o.Chrome.Path, args...)
	cmd.Env = browserEnv(o.Environ)
	stderr := &capBuffer{}
	cmd.Stderr = stderr
	// Helpers inherit the stderr pipe; bound the wait for it after exit.
	cmd.WaitDelay = 2 * time.Second
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return nil, wrapError(CodeLaunchFailed, err, "cannot start %s", o.Chrome.Path)
	}
	p := &Process{PID: cmd.Process.Pid, Profile: profile, Port: port, Version: o.Chrome.Version, done: make(chan struct{}), stderr: stderr}
	go func() {
		err := cmd.Wait()
		if ps := cmd.ProcessState; ps != nil {
			_ = err // a helper holding the stderr pipe open reports ErrWaitDelay; the status is what counts
			p.exit.Store(&childExit{clean: ps.Success(), status: ps.String()})
		}
		close(p.done)
	}()
	p.Start, _ = processStart(context.Background(), p.PID)
	return p, nil
}

// awaitEndpoint polls until the endpoint answers and is verified, the child
// exits, a foreign listener appears or ctx ends.
func (p *Process) awaitEndpoint(ctx context.Context) error {
	t := time.NewTicker(endpointPoll)
	defer t.Stop()
	for {
		select {
		case <-p.done:
			return newError(CodeLaunchFailed, "Chrome exited before its debugging endpoint answered: %s", p.stderr.head(failureTextKeep))
		case <-ctx.Done():
			return newError(CodeLaunchTimeout, "Chrome did not answer within the launch deadline: %s", p.stderr.head(failureTextKeep))
		case <-t.C:
		}
		c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p.Port)), 200*time.Millisecond)
		if err != nil {
			continue
		}
		_ = c.Close()
		doc, err := verifyEndpoint(ctx, p.PID, p.Start, p.Profile, p.Port)
		if err != nil {
			if c, _ := CodeOf(err); c == CodeEndpointUnverified {
				return err
			}
			continue // the listener is ours but not answering HTTP yet
		}
		p.wsURL = doc.WSURL
		return nil
	}
}

var endpointClient = &http.Client{
	Timeout:   2 * time.Second,
	Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true},
}

type versionDoc struct {
	Browser string `json:"Browser"`
	WSURL   string `json:"webSocketDebuggerUrl"`
}

// verifyEndpoint proves the DevTools endpoint on port belongs to the process
// with pid: the only listener is that pid, its start time is unchanged, its
// command line carries this profile and port, and the advertised WebSocket
// address is a loopback address on the same port.
func verifyEndpoint(ctx context.Context, pid int, start, profile string, port int) (versionDoc, error) {
	unverified := func(format string, args ...any) error {
		return newError(CodeEndpointUnverified, format, args...)
	}
	ls, err := listPortOwners(ctx, port)
	if err != nil {
		// The tool failed; that says nothing about who owns the port.
		return versionDoc{}, wrapError(CodeBrowserDisconnected, err, "cannot list listeners on the debugging port")
	}
	if len(ls) == 0 {
		return versionDoc{}, unverified("nothing listens on the debugging port")
	}
	for _, l := range ls {
		if l.PID != pid {
			return versionDoc{}, unverified("debugging port is held by another process")
		}
		if !loopbackAddr(l.Addr) {
			return versionDoc{}, unverified("the debugging port is bound beyond the loopback interface")
		}
	}
	if err := verifyIdentity(ctx, pid, start, profile, port); err != nil {
		return versionDoc{}, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/json/version", port), nil)
	resp, err := endpointClient.Do(req)
	if err != nil {
		return versionDoc{}, wrapError(CodeBrowserDisconnected, err, "debugging endpoint not answering")
	}
	defer resp.Body.Close()
	var doc versionDoc
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&doc); err != nil {
		return versionDoc{}, wrapError(CodeBrowserDisconnected, err, "debugging endpoint returned no version document")
	}
	u, err := url.Parse(doc.WSURL)
	if err != nil || u.Scheme != "ws" || u.Host != net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) {
		return versionDoc{}, unverified("debugging address is not a loopback address on the selected port")
	}
	return doc, nil
}

// Seams for the process tools, replaced by tests.
var (
	listPortOwners = listeners
	startOf        = processStart
	commandOf      = processCommand
)

// verifyIdentity proves pid is the process a record describes: its start time
// is unchanged and its command line carries this profile and port. A failure
// of the process tool is reported as such, never as a mismatch.
func verifyIdentity(ctx context.Context, pid int, start, profile string, port int) error {
	st, err := startOf(ctx, pid)
	if err != nil && processAlive(pid) {
		return wrapError(CodeBrowserDisconnected, err, "cannot read the browser process start time")
	}
	if err != nil || st != start {
		return newError(CodeEndpointUnverified, "browser process identity changed")
	}
	cmdline, err := commandOf(ctx, pid)
	if err != nil && processAlive(pid) {
		return wrapError(CodeBrowserDisconnected, err, "cannot read the browser process command line")
	}
	if err != nil || !commandRunsProfile(cmdline, profile, port) {
		return newError(CodeEndpointUnverified, "browser process does not run this profile and port")
	}
	return nil
}

// loopbackAddr reports whether an lsof socket name such as 127.0.0.1:9222 or
// [::1]:9222 is a loopback address; a wildcard bind is not.
func loopbackAddr(a string) bool {
	host, _, err := net.SplitHostPort(a)
	if err != nil {
		return false
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// commandRunsProfile reports whether a command line carries exactly this
// profile and this port. The arguments are matched whole: launchArgs places the
// profile and the port next to each other, so a longer profile path or a
// longer port number that merely starts with these cannot match.
func commandRunsProfile(cmdline, profile string, port int) bool {
	want := "--user-data-dir=" + profile + " --remote-debugging-port=" + strconv.Itoa(port)
	i := strings.Index(" "+cmdline, " "+want)
	if i < 0 {
		return false
	}
	rest := (" " + cmdline)[i+1+len(want):]
	return rest == "" || rest[0] == ' '
}

// terminate kills the process group of a child this launch started and waits
// for it to be reaped. It never touches any other process.
func (p *Process) terminate() {
	select {
	case <-p.done:
		return
	default:
	}
	p.forceKill()
	select {
	case <-p.done:
	case <-time.After(killGrace):
	}
}

// forceKill kills the browser after re-verifying that pid still names the
// process this value describes: a recycled pid is never signalled. The whole
// group is killed only when the process leads its own group, as a launched
// browser does.
func (p *Process) forceKill() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if p.Start != "" {
		if st, err := processStart(ctx, p.PID); err != nil || st != p.Start {
			return
		}
	}
	if g, err := processGroup(ctx, p.PID); err == nil && g == p.PID {
		killGroup(p.PID)
		return
	}
	killPID(p.PID)
}

// StopOutcome reports how a stop ended.
type StopOutcome string

// Stop outcomes.
const (
	StopClosed   StopOutcome = "closed"
	StopKilled   StopOutcome = "killed"
	StopDetached StopOutcome = "detached"
)

// stop asks the browser to close with graceful, waits up to stopGrace (or
// until ctx ends), then force-kills and waits up to killGrace more. Exit is
// observed from the process status, not from a signal having been sent.
func (p *Process) stop(ctx context.Context, graceful func()) StopOutcome {
	select {
	case <-p.done:
		return StopClosed
	default:
	}
	if graceful != nil {
		graceful()
	}
	select {
	case <-p.done:
		// A renderer busy in a script can outlive the browser process that quit
		// cleanly; the group the browser led is still ours to end. Durable state
		// was committed before the quit, so there is nothing to wait for.
		killGroup(p.PID)
		return StopClosed
	case <-ctx.Done():
	case <-time.After(stopGrace):
	}
	p.forceKill()
	select {
	case <-p.done:
		return StopKilled
	case <-time.After(killGrace):
		return StopDetached
	}
}

// Record is the instance record kept for adoption after a server restart.
type Record struct {
	PID           int    `json:"pid"`
	Start         string `json:"start"`
	Profile       string `json:"profile"`
	Port          int    `json:"port"`
	ChromeVersion string `json:"chrome_version"`
	ApplyWindows  []int  `json:"apply_windows,omitempty"`
	ScanWindows   []int  `json:"scan_windows,omitempty"`
	// Abandoned is set when the connection is lost while a tab creation was
	// under way or left unanswered, whose blank tab the browser may have made
	// all the same, and Tabs then lists the pages the instance knew. The
	// adopter closes the blank pages without an opener that are none of
	// those (adoptExisting).
	Abandoned bool     `json:"abandoned,omitempty"`
	Tabs      []string `json:"tabs,omitempty"`
}

// WriteRecord replaces the record atomically with mode 0600. The WebSocket
// path is never persisted; it is rediscovered on adoption.
func WriteRecord(path string, r Record) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadRecord loads a record; a missing or malformed file reads as no record.
func ReadRecord(path string) (Record, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Record{}, false
	}
	var r Record
	if json.Unmarshal(b, &r) != nil || r.PID <= 0 || r.Port <= 0 {
		return Record{}, false
	}
	return r, true
}

// adoptVersionWait bounds the version request made while adopting.
const adoptVersionWait = 2 * time.Second

// watchInterval is how often an adopted process is checked for exit.
var watchInterval = 250 * time.Millisecond

// identityEvery is how many exit checks pass between identity checks, which
// cost a process listing and catch a recycled pid.
const identityEvery = 20

// adopt attaches to the surviving process named by rec, which must run the
// configured profile (canonical path) and never the everyday one, after the same owner
// checks a fresh launch passes. A recycled pid, another profile or another
// listener fails closed before any DevTools command is sent. ours says whether
// the record's process was proven to be our browser (pid, start time and
// command line all match the record) even though it could not be adopted.
func adopt(ctx context.Context, rec Record, wantMajor int, profile string) (p *Process, ours bool, err error) {
	if err := platformSupported(); err != nil {
		return nil, false, err
	}
	if rec.Profile != profile {
		return nil, false, newError(CodeProfileUnsafe, "the recorded profile is not the configured profile")
	}
	home, _ := os.UserHomeDir()
	if overlapsEveryday(rec.Profile, home, os.Getenv) {
		return nil, false, newError(CodeProfileUnsafe, "the recorded profile overlaps the everyday Chrome data directory")
	}
	if !processAlive(rec.PID) {
		return nil, false, newError(CodeEndpointUnverified, "recorded browser process is gone")
	}
	ctx, cancel := context.WithTimeout(ctx, adoptVersionWait+3*time.Second)
	defer cancel()
	if err := verifyIdentity(ctx, rec.PID, rec.Start, rec.Profile, rec.Port); err != nil {
		return nil, false, err
	}
	doc, err := verifyEndpoint(ctx, rec.PID, rec.Start, rec.Profile, rec.Port)
	if err != nil {
		return nil, true, err
	}
	if wantMajor > 0 && !strings.HasPrefix(doc.Browser, "Chrome/"+strconv.Itoa(wantMajor)+".") {
		return nil, true, newError(CodeBrowserVersion, "running Chrome is %q, not major version %d", doc.Browser, wantMajor)
	}
	if rec.ChromeVersion == "" {
		rec.ChromeVersion = strings.TrimPrefix(doc.Browser, "Chrome/")
	}
	p = newAdopted(rec, doc.WSURL)
	return p, true, nil
}

// transient reports a failure that says nothing about the browser itself: a
// process tool or the version request failed, so the record must be kept.
func transient(err error) bool {
	c, ok := CodeOf(err)
	return ok && c == CodeBrowserDisconnected
}

func newAdopted(rec Record, ws string) *Process {
	p := &Process{PID: rec.PID, Start: rec.Start, Profile: rec.Profile, Port: rec.Port, Version: rec.ChromeVersion, wsURL: ws, done: make(chan struct{})}
	var wctx context.Context
	wctx, p.unwatch = context.WithCancel(context.Background())
	go p.watch(wctx)
	return p
}

// alive reports whether the process has not been seen to exit. A recycled pid
// counts as alive: adoption and retirement verify the identity before acting.
func (p *Process) alive() bool {
	select {
	case <-p.done:
		return false
	default:
		return processAlive(p.PID)
	}
}

// stopWatching ends the exit watcher of an adopted process.
func (p *Process) stopWatching() {
	if p.unwatch != nil {
		p.unwatch()
	}
}

// watch closes done once the process is gone or its pid belongs to another
// process, and returns without closing it when ctx ends first.
func (p *Process) watch(ctx context.Context) {
	t := time.NewTicker(watchInterval)
	defer t.Stop()
	for n := 1; ; n++ {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if !processAlive(p.PID) {
			close(p.done)
			return
		}
		if p.Start != "" && n%identityEvery == 0 {
			c, cancel := context.WithTimeout(ctx, 3*time.Second)
			st, err := startOf(c, p.PID)
			cancel()
			if err == nil && st != p.Start {
				close(p.done)
				return
			}
		}
	}
}

// retire stops the browser a record names, after the same identity check a
// signal always gets, and waits for it to leave.
func retire(ctx context.Context, rec Record) StopOutcome {
	p := &Process{PID: rec.PID, Start: rec.Start, Profile: rec.Profile, Port: rec.Port, done: make(chan struct{})}
	wctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.watch(wctx)
	return p.stop(ctx, func() {
		c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if st, err := startOf(c, p.PID); err == nil && st == p.Start {
			askToQuit(p.PID)
		}
	})
}
