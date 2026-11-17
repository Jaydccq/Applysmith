package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"applysmith/internal/api"
	"applysmith/internal/api/wire"
	"applysmith/internal/llm"
	"applysmith/internal/onboarding"
	"applysmith/internal/remote/tunnel"
	"applysmith/internal/vault"
	"applysmith/internal/workspace"
)

// realHome is the developer's home directory, kept only so a test can check
// that it does not resolve it; realEnviron is the environment before TestMain
// moved HOME, for the go toolchain a test runs.
var (
	realHome, _ = os.UserHomeDir()
	realEnviron = os.Environ()
)

// TestMain points HOME at a temporary folder for the whole package, so code
// that falls back to the user's own home or configuration directory (the
// server's onboarding, document toolchain and CLI install paths, the browser
// launcher) never reads or writes the developer's.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "applysmith-cli-home-")
	if err != nil {
		panic(err)
	}
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME"} {
		_ = os.Unsetenv(k)
	}
	_ = os.Setenv("HOME", home)
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}

// testEnv runs commands in-process against a shared fake keychain.
type testEnv struct {
	t       *testing.T
	backend *vault.Memory
	vars    map[string]string
	ws      string
	// stdin feeds the command; home and config stand in for the user's own
	// directories; hook replaces the machine probes of the setup environment.
	stdin        string
	home, config string
	hook         func(*onboarding.Env)
	llmOpts      []llm.Option
	interactive  func(*exec.Cmd) error
	remoteTunnel func(tunnel.Config) api.TunnelProcess
	remoteEvery  time.Duration
	gmailDial    func(ctx context.Context) (net.Conn, error)
}

type output struct {
	code           int
	stdout, stderr string
}

// newTestEnv starts with an empty workspace folder, which every command but
// init and setup needs to exist (newBareTestEnv has none).
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	te := newBareTestEnv(t)
	if err := os.Mkdir(te.ws, 0o700); err != nil {
		t.Fatal(err)
	}
	return te
}

// newInitTestEnv is newTestEnv with the workspace initialised.
func newInitTestEnv(t *testing.T) *testEnv {
	t.Helper()
	te := newTestEnv(t)
	if o := te.run(context.Background(), "init", "--workspace", te.ws); o.code != 0 {
		t.Fatalf("init: %d %s", o.code, o.stderr)
	}
	return te
}

func newBareTestEnv(t *testing.T) *testEnv {
	t.Helper()
	return &testEnv{t: t, backend: vault.NewMemory(), vars: map[string]string{}, ws: filepath.Join(t.TempDir(), "ws"),
		home: t.TempDir(), config: t.TempDir()}
}

// env takes any writers; commands that start the evaluation pool write
// stderr from its workers, so their callers pass a syncBuffer.
func (te *testEnv) env(out, errw io.Writer) *env {
	dial := te.gmailDial
	if dial == nil {
		// A test reaches Gmail only through a fake it wires itself.
		dial = func(context.Context) (net.Conn, error) { return nil, errors.New("this test wired no fake Gmail") }
	}
	return &env{stdout: out, stderr: errw, backend: te.backend, getenv: func(k string) string { return te.vars[k] },
		stdin: strings.NewReader(te.stdin), homeDir: te.home, configDir: te.config, hook: te.hook, llmOptions: te.llmOpts, interactive: te.interactive, remoteTunnel: te.remoteTunnel,
		remoteEvery: te.remoteEvery, gmailDial: dial}
}

func (te *testEnv) run(ctx context.Context, args ...string) output {
	var out, errw syncBuffer
	code := te.env(&out, &errw).run(ctx, args)
	return output{code, out.String(), errw.String()}
}

// syncBuffer lets the test read what a running command printed.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type runningServer struct {
	te     *testEnv
	cancel context.CancelFunc
	done   chan int
	stdout *syncBuffer
	stderr *syncBuffer
	origin string
}

// startServe runs `applysmith serve` on an ephemeral port and waits until it
// answers health with status ok.
func (te *testEnv) startServe() *runningServer {
	te.t.Helper()
	// Code under the server that falls back to the user's own directories
	// must land in the package's temporary home (TestMain).
	if home, err := os.UserHomeDir(); err != nil || home == realHome {
		te.t.Fatalf("the test server would resolve the developer's home %q (%v)", home, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	rs := &runningServer{te: te, cancel: cancel, done: make(chan int, 1), stdout: &syncBuffer{}, stderr: &syncBuffer{}}
	e := te.env(rs.stdout, rs.stderr)
	e.dashboard = testDashboard()
	go func() { rs.done <- e.run(ctx, []string{"serve", "--workspace", te.ws, "--port", "0"}) }()
	te.t.Cleanup(func() { rs.stop() })

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case code := <-rs.done:
			te.t.Fatalf("serve exited with %d: %s", code, rs.stderr.String())
		default:
		}
		if info, err := api.ReadLockInfo(filepath.Join(te.ws, ".applysmith")); err == nil {
			if h, ok := api.ProbeServer(strings.TrimPrefix(info.Origin, "http://")); ok && h.Status == "ok" {
				rs.origin = info.Origin
				return rs
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	te.t.Fatalf("serve did not become healthy: %s", rs.stderr.String())
	return nil
}

func (rs *runningServer) stop() int {
	rs.cancel()
	select {
	case code := <-rs.done:
		rs.done <- code
		return code
	case <-time.After(20 * time.Second):
		rs.te.t.Error("serve did not stop")
		return -1
	}
}

func TestVersionAndUsage(t *testing.T) {
	te := newTestEnv(t)
	if o := te.run(context.Background(), "version"); o.code != 0 || !strings.HasPrefix(o.stdout, "applysmith ") {
		t.Errorf("version: %+v", o)
	}
	if o := te.run(context.Background()); o.code != 2 || !strings.Contains(o.stderr, "usage") {
		t.Errorf("no command: %+v", o)
	}
	if o := te.run(context.Background(), "nonsense"); o.code != 2 {
		t.Errorf("unknown command: %+v", o)
	}
	if o := te.run(context.Background(), "trackr"); o.code != 2 || !strings.Contains(o.stderr, `did you mean "tracker"?`) {
		t.Errorf("typo: %+v", o)
	}
	// A word of a hyphenated command names it.
	if o := te.run(context.Background(), "followup"); o.code != 2 || !strings.Contains(o.stderr, `did you mean "followup-draft"?`) {
		t.Errorf("part of a name: %+v", o)
	}
	if o := te.run(context.Background(), "serve", "--bogus"); o.code != 2 {
		t.Errorf("bad flag: %+v", o)
	}
}

// An explicit --help is not a usage error: exit 0, the help on standard
// output, and a schedule subcommand lists only its own flags. A mistyped
// command asked for help is refused as without it.
func TestExplicitHelpExitsZeroWithItsOwnFlags(t *testing.T) {
	te := newTestEnv(t)
	for _, args := range [][]string{{"scan", "--help"}, {"tracker", "--help"}, {"serve", "-h"}, {"schedule", "packs", "--help"}} {
		if o := te.run(context.Background(), args...); o.code != 0 || o.stderr != "" || o.stdout == "" {
			t.Errorf("%v: %+v", args, o)
		}
	}
	// Help for a word that names no command is still a usage error.
	for _, args := range [][]string{{"trackr", "--help"}, {"tracker", "lst", "-h"}, {"help", "trackr"}} {
		if o := te.run(context.Background(), args...); o.code != 2 || o.stdout != "" || !strings.Contains(o.stderr, `did you mean "`) {
			t.Errorf("%v: %+v", args, o)
		}
	}
	o := te.run(context.Background(), "schedule", "packs", "--help")
	if !strings.Contains(o.stdout, "-json") || strings.Contains(o.stdout, "-rrule") || strings.Contains(o.stdout, "-kind") {
		t.Errorf("schedule packs --help lists flags it does not take:\n%s", o.stdout)
	}
	if o := te.run(context.Background(), "schedule", "add", "--help"); !strings.Contains(o.stdout, "-rrule") {
		t.Errorf("schedule add --help lacks its flags:\n%s", o.stdout)
	}
	if o := te.run(context.Background(), "help", "doctor"); !strings.Contains(o.stdout, "\n  --json\n") {
		t.Errorf("help doctor: %+v", o)
	}
	if o := te.run(context.Background(), "setup", "--help"); !strings.Contains(o.stdout, "applysmith setup status") {
		t.Errorf("setup --help: %+v", o)
	}
	// Help opens no workspace, so a running server does not stand in its way.
	lock, err := api.AcquireLock(workspace.Open(te.ws).StateDir())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	for _, cmd := range []string{"inbox", "blocked", "registry"} {
		if o := te.run(context.Background(), cmd, "--help", "--workspace", te.ws); o.code != 0 || !strings.Contains(o.stdout, "applysmith "+cmd+" ") {
			t.Errorf("help %s while a server holds the workspace: %+v", cmd, o)
		}
	}
}

// Every command a script may run against a workspace accepts --workspace,
// including the ones that read none.
func TestWorkspaceFlagIsAcceptedByCommandsThatReadNone(t *testing.T) {
	te := newTestEnv(t)
	dir := t.TempDir()
	for _, args := range [][]string{
		{"documents", "toolchain", "check"}, {"setup", "schema", "identity"}, {"backup", "verify", dir}, {"extension", "export", filepath.Join(dir, "ext")},
	} {
		o := te.run(context.Background(), append(args, "--workspace", te.ws)...)
		if strings.Contains(o.stderr, "not defined") || strings.Contains(o.stderr, "usage") {
			t.Errorf("%v rejected --workspace: %+v", args, o)
		}
	}
}

func TestServeAnswersHealthAndStopsCleanly(t *testing.T) {
	te := newTestEnv(t)
	rs := te.startServe()
	if !strings.Contains(rs.stdout.String(), rs.origin) {
		t.Errorf("serve did not print its address: %q", rs.stdout.String())
	}
	resp, err := http.Get(rs.origin + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Status  string `json:"status"`
			BuildID string `json:"buildId"`
			PathKey string `json:"workspaceId"`
			Version string `json:"version"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	if !env.OK || env.Data.Status != "ok" || env.Data.BuildID == "" || env.Data.Version != "dev" || env.Data.PathKey != workspace.PathKey(te.ws) {
		t.Errorf("health %+v", env)
	}
	if code := rs.stop(); code != 0 {
		t.Errorf("exit code %d: %s", code, rs.stderr.String())
	}
	if held, _ := api.LockHeld(filepath.Join(te.ws, ".applysmith")); held {
		t.Error("the lock outlived the server")
	}
}

func TestSecondServeOnTheSameWorkspaceExitsWithALockError(t *testing.T) {
	te := newTestEnv(t)
	rs := te.startServe()
	o := te.run(context.Background(), "serve", "--workspace", te.ws, "--port", "0")
	if o.code == 0 || !strings.Contains(o.stderr, "server_already_running") || !strings.Contains(o.stderr, rs.origin) {
		t.Fatalf("second serve: %+v", o)
	}
}

func TestServeRefusesANonLoopbackHost(t *testing.T) {
	te := newTestEnv(t)
	o := te.run(context.Background(), "serve", "--workspace", te.ws, "--host", "0.0.0.0", "--port", "0")
	if o.code == 0 || !strings.Contains(o.stderr, "loopback only") {
		t.Fatalf("%+v", o)
	}
	if held, _ := api.LockHeld(filepath.Join(te.ws, ".applysmith")); held {
		t.Error("a refused start left the lock held")
	}
}

func TestServePortComesFromTheEnvironment(t *testing.T) {
	te := newTestEnv(t)
	te.vars["APPLYSMITH_PORT"] = "not-a-port"
	if o := te.run(context.Background(), "serve", "--workspace", te.ws); o.code == 0 || !strings.Contains(o.stderr, "APPLYSMITH_PORT") {
		t.Fatalf("%+v", o)
	}
	if got, err := (&env{getenv: func(string) string { return "" }}).defaultPort(); err != nil || got != 47319 {
		t.Errorf("default port %d, %v", got, err)
	}
	if got, err := (&env{getenv: func(string) string { return "8123" }}).defaultPort(); err != nil || got != 8123 {
		t.Errorf("APPLYSMITH_PORT port %d, %v", got, err)
	}
}

func TestDoctorFramework(t *testing.T) {
	te := newTestEnv(t)
	ctx := context.Background()
	o := te.run(ctx, "doctor", "--workspace", te.ws, "--port", "0")
	if o.code != 1 || !strings.HasPrefix(o.stdout, "Applysmith supports macOS") || !strings.Contains(o.stdout, "workspace.files") {
		t.Fatalf("empty workspace: %+v", o)
	}
	o = te.run(ctx, "doctor", "--workspace", te.ws, "--port", "0", "--json", "--only", "runtime")
	var rep struct {
		Results []struct {
			ID, Status string
		}
	}
	if err := json.Unmarshal([]byte(o.stdout), &rep); err != nil || len(rep.Results) != 4 {
		t.Fatalf("json report: %+v (%v)", o, err)
	}
	if o.code != 0 {
		t.Errorf("runtime checks pass on a fresh workspace, exit code %d\n%s", o.code, o.stdout)
	}
	if o := te.run(ctx, "doctor", "--workspace", te.ws, "--only", "nonsense"); o.code != 2 {
		t.Errorf("unknown check: %+v", o)
	}
	if o := te.run(ctx, "doctor", "--live", "--offline"); o.code != 2 {
		t.Errorf("live and offline: %+v", o)
	}
	te.vars["APPLYSMITH_PORT"] = "x"
	if o := te.run(ctx, "doctor"); o.code != 4 {
		t.Errorf("unable to run: %+v", o)
	}
}

func TestBackupOfflineVerifyAndRestore(t *testing.T) {
	te := newTestEnv(t)
	ctx := context.Background()
	if o := te.run(ctx, "backup", "create", "--workspace", te.ws, "--out", filepath.Join(t.TempDir(), "b")); o.code != 1 || !strings.Contains(o.stderr, "no database") {
		t.Fatalf("backup before any database: %+v", o)
	}
	te.startServe().stop()
	if err := os.WriteFile(filepath.Join(te.ws, "cv.md"), []byte("Alex Example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "backup")
	o := te.run(ctx, "backup", "create", "--workspace", te.ws, "--out", out)
	if o.code != 0 || !strings.Contains(o.stdout, "backup written") || !strings.Contains(o.stdout, "not included") {
		t.Fatalf("create: %+v", o)
	}
	if o := te.run(ctx, "backup", "create", "--workspace", te.ws, "--out", out); o.code == 0 {
		t.Errorf("an existing output directory was accepted: %+v", o)
	}
	if o := te.run(ctx, "backup", "verify", out); o.code != 0 || !strings.Contains(o.stdout, "verified") {
		t.Fatalf("verify: %+v", o)
	}
	to := filepath.Join(t.TempDir(), "restored")
	if o := te.run(ctx, "backup", "restore", out, "--to", to); o.code != 0 {
		t.Fatalf("restore: %+v", o)
	}
	if b, err := os.ReadFile(filepath.Join(to, "cv.md")); err != nil || string(b) != "Alex Example\n" {
		t.Errorf("restored cv.md: %q %v", b, err)
	}
	if o := te.run(ctx, "backup", "restore", out, "--to", to); o.code == 0 {
		t.Errorf("restoring into an existing workspace succeeded")
	}
	// The restored workspace starts as a new identity.
	te2 := &testEnv{t: t, backend: te.backend, vars: te.vars, ws: to}
	te2.startServe()

	if err := os.WriteFile(filepath.Join(out, "tampered"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if o := te.run(ctx, "backup", "verify", out); o.code == 0 {
		t.Errorf("a backup with an unlisted file verified: %+v", o)
	}
	if o := te.run(ctx, "backup"); o.code != 2 {
		t.Errorf("usage: %+v", o)
	}
}

func TestBackupWhileAServerRunsGoesThroughTheServer(t *testing.T) {
	te := newTestEnv(t)
	te.startServe()
	out := filepath.Join(t.TempDir(), "live")
	o := te.run(context.Background(), "backup", "create", "--workspace", te.ws, "--out", out)
	if o.code != 0 || !strings.Contains(o.stdout, "by the running server") {
		t.Fatalf("%+v", o)
	}
	if o := te.run(context.Background(), "backup", "verify", out); o.code != 0 {
		t.Fatalf("verify: %+v", o)
	}
}

// A lock document that the server did not write is not believed: the start
// reports a locked workspace, and the token is never read.
func TestUnverifiedLockDocumentNeverLeadsToTheToken(t *testing.T) {
	te := newTestEnv(t)
	for _, origin := range []string{"http://203.0.113.9:47319", "https://127.0.0.1:1", "http://127.0.0.1.example.com:1", "http://user@127.0.0.1:1", "http://[::2]:1"} {
		lock, err := api.AcquireLock(filepath.Join(te.ws, ".applysmith"))
		if err != nil {
			t.Fatal(err)
		}
		if err := lock.WriteInfo(api.LockInfo{PID: 1, Port: 1, Origin: origin}); err != nil {
			t.Fatal(err)
		}
		te.backend.FailWith(vault.ErrDenied) // reading the token would fail loudly
		o := te.run(context.Background(), "backup", "create", "--workspace", te.ws, "--out", filepath.Join(t.TempDir(), "b"))
		te.backend.FailWith(nil)
		lock.Release()
		if o.code != 1 || strings.Contains(o.stderr, "keychain") {
			t.Errorf("origin %s: %+v", origin, o)
		}
	}
}

// Whatever listens at the address in the lock file must prove it is this
// workspace's server before the bearer token goes there.
func TestLiveBackupSendsNoTokenToAServerThatIsNotThisWorkspaces(t *testing.T) {
	te := newTestEnv(t)
	ws := workspace.Open(te.ws)
	var sawAuth atomic.Bool
	imposter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			sawAuth.Store(true)
		}
		_ = json.NewEncoder(w).Encode(wire.Success[wire.Health]{OK: true, Data: wire.Health{
			Status: wire.HealthOK, APIVersion: wire.APIVersion, PathKey: "aaaaaaaaaaaa"}})
	}))
	defer imposter.Close()
	id, err := ws.ID()
	if err != nil {
		t.Fatal(err)
	}
	v, _ := vault.New(id, te.backend, vault.WithGetenv(func(string) string { return "" }))
	if _, err := v.Set(vault.APIToken, strings.Repeat("t", 43)); err != nil {
		t.Fatal(err)
	}
	lock, err := api.AcquireLock(ws.StateDir())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if err := lock.WriteInfo(api.LockInfo{PID: 1, Port: 1, PathKey: workspace.PathKey(te.ws), Origin: imposter.URL}); err != nil {
		t.Fatal(err)
	}
	var out, errw bytes.Buffer
	code := te.env(&out, &errw).backupLive(context.Background(), ws, filepath.Join(t.TempDir(), "b"))
	if code != 1 || !strings.Contains(errw.String(), "answers for another workspace") || sawAuth.Load() {
		t.Errorf("%d %s (token sent: %v)", code, errw.String(), sawAuth.Load())
	}
}

// A healthy server whose first health answer comes after the probe's timeout
// (a loaded machine) is still this workspace's server.
func TestLiveAttachWaitsOutASlowFirstHealthAnswer(t *testing.T) {
	te := newTestEnv(t)
	ws := workspace.Open(te.ws)
	var calls atomic.Int32
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			select {
			case <-time.After(3 * time.Second):
			case <-r.Context().Done():
			}
			return
		}
		_ = json.NewEncoder(w).Encode(wire.Success[wire.Health]{OK: true, Data: wire.Health{
			Status: wire.HealthOK, APIVersion: wire.APIVersion, PathKey: workspace.PathKey(te.ws)}})
	}))
	defer slow.Close()
	id, err := ws.ID()
	if err != nil {
		t.Fatal(err)
	}
	v, _ := vault.New(id, te.backend, vault.WithGetenv(func(string) string { return "" }))
	if _, err := v.Set(vault.APIToken, strings.Repeat("t", 43)); err != nil {
		t.Fatal(err)
	}
	lock, err := api.AcquireLock(ws.StateDir())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if err := lock.WriteInfo(api.LockInfo{PID: 1, Port: 1, PathKey: workspace.PathKey(te.ws), Origin: slow.URL}); err != nil {
		t.Fatal(err)
	}
	var out, errw bytes.Buffer
	if _, err := te.env(&out, &errw).attachLive(context.Background(), ws); err != nil || calls.Load() != 2 {
		t.Fatalf("attach: %v after %d health calls", err, calls.Load())
	}
}

// A command run right after `serve` meets a server that holds the lock but
// answers starting while it opens the database: it waits, says so once, and
// attaches when the server is ready; one that never gets ready is named.
func TestLiveAttachWaitsForAServerThatIsStarting(t *testing.T) {
	te := newTestEnv(t)
	ws := workspace.Open(te.ws)
	var calls atomic.Int32
	starting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status := wire.HealthStarting
		if calls.Add(1) > 3 {
			status = wire.HealthOK
		}
		_ = json.NewEncoder(w).Encode(wire.Success[wire.Health]{OK: true, Data: wire.Health{
			Status: status, APIVersion: wire.APIVersion, PathKey: workspace.PathKey(te.ws)}})
	}))
	defer starting.Close()
	id, err := ws.ID()
	if err != nil {
		t.Fatal(err)
	}
	v, _ := vault.New(id, te.backend, vault.WithGetenv(func(string) string { return "" }))
	if _, err := v.Set(vault.APIToken, strings.Repeat("t", 43)); err != nil {
		t.Fatal(err)
	}
	lock, err := api.AcquireLock(ws.StateDir())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if err := lock.WriteInfo(api.LockInfo{PID: 1, Port: 1, PathKey: workspace.PathKey(te.ws), Origin: starting.URL}); err != nil {
		t.Fatal(err)
	}
	var out, errw bytes.Buffer
	if _, err := te.env(&out, &errw).attachLive(context.Background(), ws); err != nil || strings.Count(errw.String(), "waiting up to") != 1 {
		t.Fatalf("attach: %v; stderr %q", err, errw.String())
	}
	defer func(d time.Duration) { serverStartWait = d }(serverStartWait)
	serverStartWait = 300 * time.Millisecond
	calls.Store(-1000)
	errw.Reset()
	if _, err := te.env(&out, &errw).attachLive(context.Background(), ws); err == nil || !strings.Contains(err.Error(), "did not become ready") || !strings.Contains(err.Error(), "database") {
		t.Fatalf("a server that never gets ready: %v", err)
	}
	// Ctrl-C ends the wait at once instead of after serverStartWait.
	serverStartWait = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	begin := time.Now()
	if _, err := te.env(&out, &errw).attachLive(ctx, ws); !errors.Is(err, context.Canceled) || time.Since(begin) > 10*time.Second {
		t.Fatalf("an interrupted wait: %v after %s", err, time.Since(begin))
	}
}

// link is the dashboard address `serve` printed.
func (rs *runningServer) link() string {
	rs.te.t.Helper()
	for _, line := range strings.Split(rs.stdout.String(), "\n") {
		if rest, ok := strings.CutPrefix(line, "Open the dashboard: "); ok {
			return rest
		}
	}
	rs.te.t.Fatalf("serve printed no dashboard link:\n%s", rs.stdout.String())
	return ""
}

// dashboardDocument opens the printed link and fetches the document with the
// session it issued.
func (rs *runningServer) dashboardDocument() (*http.Response, []byte) {
	rs.te.t.Helper()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	opened, err := client.Get(rs.link())
	if err != nil {
		rs.te.t.Fatal(err)
	}
	opened.Body.Close()
	req, _ := http.NewRequest("GET", rs.origin+"/", nil)
	for _, c := range opened.Cookies() {
		req.AddCookie(c)
	}
	resp, err := client.Do(req)
	if err != nil {
		rs.te.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, body
}

func TestServePrintsTheDashboardLinkThatOpensOnce(t *testing.T) {
	te := newTestEnv(t)
	rs := te.startServe()
	link := rs.link()
	if !strings.HasPrefix(link, rs.origin+"/?launch=") {
		t.Fatalf("serve output:\n%s", rs.stdout.String())
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for i, want := range []bool{true, false} {
		resp, err := client.Get(link)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if got := len(resp.Cookies()) > 0; got != want {
			t.Errorf("use %d: session issued = %v, want %v", i+1, got, want)
		}
	}
}

// A flag a subcommand does not take, or an argument a command takes none of,
// is a usage error (exit 2) instead of being dropped.
func TestFlagsAndArgumentsACommandDoesNotTakeAreRefused(t *testing.T) {
	te := newInitTestEnv(t)
	for _, args := range [][]string{
		{"gmail", "status", "--yes"}, {"remote", "off", "--yes"}, {"doctor", "extra"},
	} {
		if o := te.run(context.Background(), append(args, "--workspace", te.ws)...); o.code != 2 {
			t.Errorf("%v: %+v", args, o)
		}
	}
	// A refused password still says why.
	if o := te.run(context.Background(), "gmail", "connect", "--address", "a@example.com", "--password", "x", "--workspace", te.ws); o.code != 2 || !strings.Contains(o.stderr, "process listings") {
		t.Errorf("gmail connect --password: %+v", o)
	}
}
