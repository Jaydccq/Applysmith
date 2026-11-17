package tunnel

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Restart policy from the specification.
const (
	stableAfter = 60 * time.Second
	maxFailures = 20
	stopGrace   = 5 * time.Second
)

var backoff = [...]time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second}

// Procs inspects processes by identifier, for the orphan cleanup.
type Procs interface {
	Alive(pid int) bool
	Command(pid int) string
	// StartTime is when the process started; false when it cannot be known.
	StartTime(pid int) (time.Time, bool)
	Terminate(pid int) error
	Kill(pid int) error
}

// Config describes one supervisor. Zero values are the production wiring.
type Config struct {
	// Binary is "auto" or the path of the executable.
	Binary string
	// PublicURL is the required HTTPS address of the named tunnel.
	PublicURL string
	// Token returns the required stored named-tunnel token.
	Token func() (string, error)
	// StatusPath is where the status document is written.
	StatusPath string
	// Log receives the child's output; it must redact secrets itself.
	Log io.Writer

	Finder  Finder
	Starter Starter
	Now     func() time.Time
	// After returns a channel that fires after d (time.After by default).
	After func(d time.Duration) <-chan time.Time
	// Write replaces the atomic file write (tests count heartbeats with it).
	Write func(path string, st Status) error
	// OnChange is told about every state change, outside the supervisor's lock.
	OnChange func(Status)
	Environ  func() []string
}

// Supervisor starts the child, restarts it when it exits, publishes its state
// and stops it in order.
type Supervisor struct {
	cfg Config

	mu     sync.Mutex
	st     Status
	cancel context.CancelFunc
	done   chan struct{}
	retry  chan struct{}

	// writeMu orders the writes of the status document.
	writeMu sync.Mutex
}

// New returns a supervisor that has not started anything.
func New(cfg Config) *Supervisor {
	if cfg.Starter == nil {
		cfg.Starter = ExecStarter
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.After == nil {
		cfg.After = time.After
	}
	if cfg.Write == nil {
		cfg.Write = WriteFile
	}
	if cfg.Log == nil {
		cfg.Log = io.Discard
	}
	if cfg.Environ == nil {
		cfg.Environ = os.Environ
	}
	return &Supervisor{cfg: cfg, st: Status{Mode: StateStopped}, retry: make(chan struct{}, 1)}
}

// Status is the current state.
func (s *Supervisor) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st
}

// Start begins supervising. A second call while running does nothing.
func (s *Supervisor) Start() {
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.cancel, s.done = cancel, done
	s.mu.Unlock()
	s.update(func(st *Status) { *st = Status{Mode: StateStarting} })
	go func() {
		defer close(done)
		s.run(ctx)
	}()
}

// Stop ends the child with a terminate signal and, after five seconds, a kill
// signal, and returns once it is gone.
func (s *Supervisor) Stop() {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.cancel, s.done = nil, nil
	s.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
	s.update(func(st *Status) { *st = Status{Mode: StateStopped} })
}

// Retry restarts a tunnel that failed, could not start, or is waiting out its
// restart delay. In any other state it does nothing.
func (s *Supervisor) Retry() {
	if m := s.Status().Mode; m != StateError && m != StateFailed {
		return
	}
	select {
	case s.retry <- struct{}{}:
	default:
	}
}

func (s *Supervisor) update(fn func(*Status)) {
	s.mu.Lock()
	fn(&s.st)
	st := s.st
	s.mu.Unlock()
	s.write()
	if s.cfg.OnChange != nil {
		s.cfg.OnChange(st)
	}
}

// write puts the current state in the status document. Each write reads the
// state and writes it under one lock, so the document never goes back to an
// older state, whichever of a change and a heartbeat started first.
func (s *Supervisor) write() {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.Lock()
	st := s.st
	s.mu.Unlock()
	_ = s.cfg.Write(s.cfg.StatusPath, st)
}

func (s *Supervisor) run(ctx context.Context) {
	var beat sync.WaitGroup
	hb, stopHB := context.WithCancel(ctx)
	beat.Go(func() { s.heartbeat(hb) })
	defer func() {
		stopHB()
		beat.Wait()
	}()
	for {
		spec, ok := s.prepare(ctx)
		if ok && !s.loop(ctx, spec) {
			return
		}
		if ctx.Err() != nil {
			return
		}
		if !s.waitRetry(ctx) {
			return
		}
		s.update(func(st *Status) { *st = Status{Mode: StateStarting} })
	}
}

func (s *Supervisor) heartbeat(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.cfg.After(heartbeatEvery):
			s.write()
		}
	}
}

func (s *Supervisor) waitRetry(ctx context.Context) bool {
	select {
	case <-s.retry:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *Supervisor) fail(state State, reason string, err error) {
	s.update(func(st *Status) {
		st.Mode, st.Reason, st.Error, st.PID, st.PublicURL = state, reason, err.Error(), 0, ""
	})
}

// prepare finds the executable and checks the named-tunnel configuration. It
// reports false after setting an error state.
func (s *Supervisor) prepare(ctx context.Context) (Spec, bool) {
	path, version, err := s.cfg.Finder.Locate(ctx, s.cfg.Binary)
	switch {
	case errors.Is(err, ErrNotFound):
		s.fail(StateError, ReasonNotFound, err)
		return Spec{}, false
	case err != nil:
		s.fail(StateError, ReasonUnusable, err)
		return Spec{}, false
	}
	var token string
	if s.cfg.Token != nil {
		if token, err = s.cfg.Token(); err != nil {
			s.fail(StateError, ReasonStartFail, fmt.Errorf("the tunnel token could not be read: %w", err))
			return Spec{}, false
		}
	}
	token = strings.TrimSpace(token)
	if token == "" {
		s.fail(StateError, ReasonNeedsToken, errors.New("remote access requires a named Cloudflare Tunnel: set APPLYSMITH_SECRET_CLOUDFLARED_TUNNEL_TOKEN and remote_access.tunnel.public_url; Quick Tunnels do not support the dashboard's SSE streams"))
		return Spec{}, false
	}
	public, err := url.Parse(s.cfg.PublicURL)
	if err != nil || public.Scheme != "https" || public.Hostname() == "" || public.User != nil || strings.EqualFold(public.Hostname(), "trycloudflare.com") || strings.HasSuffix(strings.ToLower(public.Hostname()), ".trycloudflare.com") {
		s.fail(StateError, ReasonNeedsURL, errors.New("a named tunnel needs an HTTPS remote_access.tunnel.public_url on its configured hostname, outside trycloudflare.com"))
		return Spec{}, false
	}
	args, extra := Command(token)
	s.update(func(st *Status) { st.Version = version })
	return Spec{Path: path, Args: args, Env: ChildEnv(s.cfg.Environ(), extra)}, true
}

// loop runs the child until the context ends (false) or the restart budget is
// spent (true, in the failed state).
func (s *Supervisor) loop(ctx context.Context, spec Spec) bool {
	failures := 0
	for {
		started := s.cfg.Now()
		err := s.runChild(ctx, spec)
		if ctx.Err() != nil {
			return false
		}
		if s.cfg.Now().Sub(started) >= stableAfter {
			failures = 0
		}
		failures++
		reason := ReasonExited
		if err != nil {
			reason = ReasonStartFail
		}
		msg := "the tunnel process exited"
		if err != nil {
			msg = err.Error()
		}
		if failures >= maxFailures {
			s.update(func(st *Status) {
				st.Mode, st.Reason, st.PID, st.PublicURL = StateFailed, ReasonRetries, 0, ""
				st.ConsecutiveFailures, st.Error = failures, fmt.Sprintf("%s; %d restarts failed in a row, retry from the dashboard or `applysmith remote retry`", msg, failures)
			})
			return true
		}
		delay := backoff[min(failures, len(backoff))-1]
		restartAt := s.cfg.Now()
		s.update(func(st *Status) {
			st.Mode, st.Reason, st.PID, st.PublicURL = StateError, reason, 0, ""
			st.ConsecutiveFailures, st.LastRestartAt = failures, &restartAt
			st.Error = fmt.Sprintf("%s; restarting in %d s", msg, int(delay/time.Second))
		})
		select {
		case <-s.cfg.After(delay):
		case <-s.retry:
			failures = 0
		case <-ctx.Done():
			return false
		}
		s.update(func(st *Status) { st.Mode, st.Reason, st.Error = StateStarting, "", "" })
	}
}

// runChild runs one child to its end. A start failure comes back as an error;
// an exit, whatever its code, does not.
func (s *Supervisor) runChild(ctx context.Context, spec Spec) error {
	child, err := s.cfg.Starter(spec)
	if err != nil {
		return fmt.Errorf("the tunnel could not be started: %w", err)
	}
	started := s.cfg.Now()
	s.update(func(st *Status) { st.PID, st.StartedAt, st.PublicURL = child.PID(), &started, "" })
	scanned := make(chan struct{})
	go func() {
		defer close(scanned)
		s.scan(child.Output())
	}()
	exited := make(chan struct{})
	go func() {
		_ = child.Wait()
		close(exited)
	}()
	select {
	case <-exited:
	case <-ctx.Done():
		_ = child.Terminate()
		select {
		case <-exited:
		case <-s.cfg.After(stopGrace):
			_ = child.Kill()
			<-exited
		}
	}
	<-scanned
	return nil
}

// scan copies the child's output to the log and watches it for readiness.
func (s *Supervisor) scan(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		_, _ = io.WriteString(s.cfg.Log, line+"\n")
		if s.Status().Mode == StateRunning {
			continue
		}
		if strings.Contains(line, "Registered tunnel connection") {
			s.running(s.cfg.PublicURL)
		}
	}
	// A line over the buffer ends the scan. The rest is still read, or the
	// child blocks on its next write and its exit is never seen.
	_, _ = io.Copy(io.Discard, r)
}

func (s *Supervisor) running(url string) {
	s.update(func(st *Status) { st.Mode, st.PublicURL, st.Reason, st.Error = StateRunning, url, "", "" })
}

// startTolerance is how far a process may have started from the time the status
// file recorded for its child (the process list rounds to whole seconds).
const startTolerance = 5 * time.Second

// CleanOrphan terminates a cloudflared that an earlier server left running:
// it reads the identifier in the status file and acts only on a live process
// whose executable is cloudflared and which started when the file says the
// child did, so an identifier the system gave to another cloudflared since
// is left alone. sleep waits between checks.
func CleanOrphan(statusPath string, procs Procs, sleep func(time.Duration)) bool {
	if procs == nil {
		procs = osProcs{}
	}
	if sleep == nil {
		sleep = time.Sleep
	}
	st := ReadFile(statusPath, time.Now())
	if st.PID <= 0 || st.StartedAt == nil || !procs.Alive(st.PID) || !strings.Contains(strings.ToLower(filepath.Base(procs.Command(st.PID))), "cloudflared") {
		return false
	}
	if started, ok := procs.StartTime(st.PID); !ok || started.Sub(*st.StartedAt).Abs() > startTolerance {
		return false
	}
	_ = procs.Terminate(st.PID)
	for range int(stopGrace / (100 * time.Millisecond)) {
		if !procs.Alive(st.PID) {
			return true
		}
		sleep(100 * time.Millisecond)
	}
	_ = procs.Kill(st.PID)
	return true
}
