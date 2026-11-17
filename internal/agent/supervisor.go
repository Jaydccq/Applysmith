package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"applysmith/internal/agent/pirpc"
	"applysmith/internal/agent/sessions"
	"applysmith/internal/llm"
)

const (
	// MaxProcesses is how many pi processes may run at once: a dashboard turn,
	// a Telegram turn and a scheduled turn can overlap. Background turns hold
	// at most MaxProcesses-1 of them (Turn.background).
	MaxProcesses = 3
	// failuresUntilOff is how many starts may fail in a row before the loop
	// stays unavailable until Reset.
	failuresUntilOff = 10
)

// backoff is how long a start is refused after the nth failure in a row.
var backoff = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second}

// Router is the part of the model router the agent uses: the route a turn is
// launched with, the cap, the gateway its model calls go through, and the
// classifier call that picks the route.
type Router interface {
	Gateway
	AgentRoute(ctx context.Context, task string) (llm.AgentRoute, error)
	AgentCapBlocked(ctx context.Context, task string) bool
	DailySpend(ctx context.Context) (spend, capUSD float64)
	RecordAgentUsage(ctx context.Context, route llm.AgentRoute, u llm.AgentUsage)
	// Complete is the call of the intent classifier (llm.IntentTask).
	Complete(ctx context.Context, req llm.Request) (*llm.Response, error)
}

// SupervisorConfig wires the supervisor.
type SupervisorConfig struct {
	// PiPath is the pi executable; empty looks `pi` up on PATH.
	PiPath string
	// AgentDir is the product-owned directory pi keeps its files in.
	AgentDir string
	// BaseURL returns the loopback origin the extension calls.
	BaseURL  func() string
	Router   Router
	Sessions *sessions.Store
	Bridge   *Bridge
	Launcher Launcher
	Log      *slog.Logger
	// Environ is the parent environment pi's is built from; nil is os.Environ.
	Environ func() []string
	// Redact scrubs pi's diagnostics before they reach the log.
	Redact func(string) string
}

// Supervisor starts and watches the pinned pi process of each turn: at most
// three at once, one per turn, ended when the turn ends.
type Supervisor struct {
	cfg SupervisorConfig
	*processes
	// lent marks a supervisor that runs another stack's turns (Beside): each
	// holds a background place, whatever its channel.
	lent bool

	mu       sync.Mutex
	checked  bool
	failures int
	nextTry  time.Time
	logins   map[string]time.Time
}

// processes is what the supervisors of one agent directory share: the slots,
// and the routes its models.json declares.
type processes struct {
	slots chan struct{}
	// background holds a place for each background turn that holds a slot.
	background chan struct{}

	// served are the gateway routes of the pi processes started, by provider
	// name: models.json declares all of them, whichever turn wrote it last.
	modelsMu sync.Mutex
	served   map[string]llm.AgentRoute
}

// NewSupervisor returns a supervisor; nothing is started until a turn runs.
func NewSupervisor(cfg SupervisorConfig) *Supervisor {
	if cfg.Launcher == nil {
		cfg.Launcher = execLauncher{}
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Environ == nil {
		cfg.Environ = osEnviron
	}
	if cfg.Redact == nil {
		cfg.Redact = func(s string) string { return llm.Redact(s) }
	}
	return &Supervisor{cfg: cfg, processes: &processes{slots: make(chan struct{}, MaxProcesses), background: make(chan struct{}, MaxProcesses-1)}}
}

// Beside returns a supervisor for the turns of another stack (a probe's bridge
// and record) that runs them as this one's processes: in the same agent
// directory, within the same three slots and never in the one kept for a
// person, and calling this server's routes, which answer them from the given
// bridge until end is called.
func (s *Supervisor) Beside(b *Bridge, ss *sessions.Store) (sup *Supervisor, end func()) {
	cfg := s.cfg
	cfg.Bridge, cfg.Sessions = b, ss
	return &Supervisor{cfg: cfg, processes: s.processes, lent: true}, s.cfg.Bridge.lend(b)
}

// UnavailableError says why the model loop cannot run and what to do.
type UnavailableError struct {
	Message string
	Fix     string
}

func (e *UnavailableError) Error() string {
	if e.Fix == "" {
		return e.Message
	}
	return e.Message + ". " + e.Fix
}

// InstallHint is the instruction printed when pi is missing or is not the
// pinned release.
func InstallHint() string {
	return fmt.Sprintf("Install pi %s: download %s from https://github.com/earendil-works/pi/releases, extract it and keep the extracted release directory intact, add that directory to PATH (or set APPLYSMITH_PI_PATH to its pi executable), then run `applysmith doctor`",
		pirpc.PinnedVersion, installAsset(pirpc.PinnedVersion))
}

func (s *Supervisor) piPath() (string, error) {
	if s.cfg.PiPath != "" {
		return s.cfg.PiPath, nil
	}
	p, err := exec.LookPath("pi")
	if err != nil {
		return "", &UnavailableError{Message: "pi is not installed", Fix: InstallHint()}
	}
	return p, nil
}

// Check runs `pi --version` and compares it with the pinned release. It is the
// start check, and what `applysmith doctor` prints.
func (s *Supervisor) Check(ctx context.Context) (version string, err error) {
	path, err := s.piPath()
	if err != nil {
		return "", err
	}
	out, err := s.cfg.Launcher.Version(ctx, path, baseEnv(s.cfg.Environ()))
	if err != nil {
		return "", &UnavailableError{Message: "pi did not report its version: " + cutRunes(err.Error(), 200), Fix: InstallHint()}
	}
	version = strings.TrimSpace(out)
	if err := pirpc.CheckVersion(out); err != nil {
		return version, &UnavailableError{Message: err.Error(), Fix: InstallHint()}
	}
	return version, nil
}

// loginTTL is how long a good answer of `pi auth check` is reused.
const loginTTL = time.Minute

// CheckLogin asks pi whether it holds a usable login for its own provider id.
// A pi: route runs only with one; the answer is reused for a minute.
func (s *Supervisor) CheckLogin(ctx context.Context, provider string) error {
	s.mu.Lock()
	if at, ok := s.logins[provider]; ok && time.Since(at) < loginTTL {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	path, err := s.piPath()
	if err != nil {
		return err
	}
	env := append(baseEnv(s.cfg.Environ()), pirpc.ChildEnv(s.cfg.AgentDir)...)
	if err := s.cfg.Launcher.AuthCheck(ctx, path, provider, env); err != nil {
		return &UnavailableError{Message: fmt.Sprintf("pi has no usable login for %s", provider), Fix: "run `applysmith agent login`, sign in to " + provider + " in pi, then ask again"}
	}
	s.mu.Lock()
	if s.logins == nil {
		s.logins = map[string]time.Time{}
	}
	s.logins[provider] = time.Now()
	s.mu.Unlock()
	return nil
}

// Reset clears the failure count, for a doctor run or a settings change.
func (s *Supervisor) Reset() {
	s.mu.Lock()
	s.failures, s.nextTry, s.checked, s.logins = 0, time.Time{}, false, nil
	s.mu.Unlock()
}

// ready runs the start check once and refuses while a failed start is backing
// off or has failed too many times.
func (s *Supervisor) ready(ctx context.Context) error {
	s.mu.Lock()
	switch {
	case s.failures >= failuresUntilOff:
		s.mu.Unlock()
		return &UnavailableError{Message: fmt.Sprintf("pi failed to start %d times in a row", failuresUntilOff), Fix: "fix it and run `applysmith doctor`"}
	case time.Now().Before(s.nextTry):
		wait := time.Until(s.nextTry).Round(time.Second)
		s.mu.Unlock()
		return &UnavailableError{Message: fmt.Sprintf("pi stopped unexpectedly; the next start is allowed in %s", wait)}
	}
	checked := s.checked
	s.mu.Unlock()
	if checked {
		return nil
	}
	if _, err := s.Check(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	s.checked = true
	s.mu.Unlock()
	return nil
}

// failed records a start or a run that ended without pi settling.
func (s *Supervisor) failed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checked = false // the version is checked again before the next start
	s.failures++
	s.nextTry = time.Now().Add(backoff[min(s.failures, len(backoff))-1])
}

// healthy clears the failure count after a turn that pi settled.
func (s *Supervisor) healthy() {
	s.mu.Lock()
	s.failures, s.nextTry = 0, time.Time{}
	s.mu.Unlock()
}

// holdsBackground reports whether the turn's process holds a background place.
func (s *Supervisor) holdsBackground(t *Turn) bool { return s.lent || t.background() }

// acquire waits for a free process slot. A background turn first waits for one
// of the MaxProcesses-1 places background turns may hold, so the last slot is
// always free for a person.
func (s *Supervisor) acquire(ctx context.Context, cancel <-chan struct{}, background bool) error {
	if background {
		if err := take(ctx, cancel, s.background); err != nil {
			return err
		}
	}
	if err := take(ctx, cancel, s.slots); err != nil {
		if background {
			<-s.background
		}
		return err
	}
	return nil
}

func (s *Supervisor) release(background bool) {
	<-s.slots
	if background {
		<-s.background
	}
}

func take(ctx context.Context, cancel <-chan struct{}, sem chan struct{}) error {
	select {
	case sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-cancel:
		return errCancelled
	}
}

var errCancelled = errors.New("cancelled")

// childEnv builds the process environment: the allowlisted part of the
// parent's, pi's own settings, and the loopback address and credential of the
// bridge, which is also the key of the gateway. No provider key is in it.
func (s *Supervisor) childEnv(token string) []string {
	env := noProxyFor(baseEnv(s.cfg.Environ()))
	env = append(env, pirpc.ChildEnv(s.cfg.AgentDir)...)
	return append(env, "APPLYSMITH_AGENT_URL="+s.cfg.BaseURL(), "APPLYSMITH_AGENT_TOKEN="+token)
}

// writeModels declares the gateway providers of every row served so far, with
// this turn's route as the latest of its row. The file is replaced whole, so a
// process starting beside this write never reads half of it.
func (s *Supervisor) writeModels(route llm.AgentRoute) error {
	s.modelsMu.Lock()
	defer s.modelsMu.Unlock()
	if s.served == nil {
		s.served = map[string]llm.AgentRoute{}
	}
	s.served[gatewayName(route)] = route
	models, err := modelsJSON(slices.Collect(maps.Values(s.served)), s.cfg.BaseURL())
	if err != nil {
		return err
	}
	if err := replaceFile(filepath.Join(s.cfg.AgentDir, "models.json"), models); err != nil {
		return fmt.Errorf("write models.json: %w", err)
	}
	return nil
}

// replaceFile puts data at path whole: it is written beside the file under a
// name of this write's own and renamed over it, so starts that write together
// never share a temporary file and a process that reads beside a write gets
// the file before it or the file after it.
func replaceFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
	}
	return err
}

// prepare writes the files a start needs and returns the launch spec.
func (s *Supervisor) prepare(route llm.AgentRoute, token string) (Spec, error) {
	path, err := s.piPath()
	if err != nil {
		return Spec{}, err
	}
	ext, err := writeExtension(s.cfg.AgentDir)
	if err != nil {
		return Spec{}, fmt.Errorf("write the extension: %w", err)
	}
	if err := s.writeModels(route); err != nil {
		return Spec{}, err
	}
	if err := writeSettings(s.cfg.AgentDir); err != nil {
		return Spec{}, err
	}
	cwd := filepath.Join(s.cfg.AgentDir, "cwd")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		return Spec{}, err
	}
	env := s.childEnv(token)
	if spansWires(route) {
		v, err := routeEnvValue(route)
		if err != nil {
			return Spec{}, err
		}
		env = append(env, routeEnv+"="+v)
	}
	return Spec{Path: path, Args: pirpc.LaunchArgs(ext, piProviderFor(route), piModelFor(route)), Env: env, Dir: cwd}, nil
}

// retrySettings is pi's settings.json, the same for every process because
// the file is shared: a retry of a turn that spans wires goes to another
// wire's model (the virtual model never picks one twice), so at most one retry
// per other wire (three wires exist), and it waits for nothing, since another
// provider is asked, not the same one again. Whether a process retries at all
// is decided per process by the handshake's set_auto_retry (pi 1.0.4 checks
// that switch before the counts), so the file's own "enabled" is never read.
const retrySettings = `{"retry":{"maxRetries":2,"baseDelayMs":0,"maxAgentDelayMs":0}}`

// writeSettings replaces pi's settings.json whole, as writeModels does.
func writeSettings(agentDir string) error {
	if err := replaceFile(filepath.Join(agentDir, "settings.json"), []byte(retrySettings)); err != nil {
		return fmt.Errorf("write settings.json: %w", err)
	}
	return nil
}
