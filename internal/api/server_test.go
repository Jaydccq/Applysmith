package api

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/vault"
	"applysmith/internal/workspace"
)

func TestLoopbackListenerBindsOneIPv4LoopbackAddress(t *testing.T) {
	h := newHarness(t)
	if got := h.srv.listener.Addr().String(); !strings.HasPrefix(got, "127.0.0.1:") {
		t.Errorf("listener is %s", got)
	}
}

func TestNonLoopbackBindRefused(t *testing.T) {
	for _, host := range []string{"0.0.0.0", "192.0.2.10", "::", "example.com", "127.0.0.2", "127.1", "[::1]", "LOCALHOST", "localhost."} {
		ws := workspace.Open(t.TempDir())
		_, err := New(Config{Workspace: ws, Vault: newVaultFor(t, ws, vault.NewMemory()), Host: host})
		if !IsStartupCode(err, CodeNonLoopbackBind) || !strings.Contains(err.Error(), "loopback only") {
			t.Errorf("host %q: %v", host, err)
		}
	}
}

func TestHostHeaderNamesOnlyAnAddressTheServerListensOn(t *testing.T) {
	h := newHarness(t)
	for host, want := range map[string]int{"127.0.0.1": 200, "localhost": 200, "[::1]": 403} {
		r := h.do(call{path: "/api/health", host: host + ":" + itoa(h.srv.Port())})
		if r.StatusCode != want {
			t.Errorf("Host %s: status %d, want %d", host, r.StatusCode, want)
		}
	}
	if !hasIPv6Loopback() {
		return
	}
	v6 := newHarness(t, func(c *Config) { c.Host = "::1" })
	if r := v6.do(call{path: "/api/health", host: "[::1]:" + itoa(v6.srv.Port())}); r.StatusCode != 200 {
		t.Errorf("Host [::1] on a ::1 server: %d", r.StatusCode)
	}
}

// startError starts a server over a fresh workspace and returns the error.
func startError(t *testing.T, mods ...func(*Config)) error {
	t.Helper()
	ws := workspace.Open(t.TempDir())
	mem := vault.NewMemory()
	cfg := Config{Workspace: ws, Vault: newVaultFor(t, ws, mem), Now: newClock().now}
	for _, m := range mods {
		m(&cfg)
	}
	srv, err := New(cfg)
	if err != nil {
		return err
	}
	if err := srv.Start(context.Background()); err != nil {
		return err
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	return nil
}

func TestPortConflicts(t *testing.T) {
	a := newHarness(t)
	err := startError(t, func(c *Config) { c.Port = a.srv.Port() })
	if !IsStartupCode(err, CodeServerAlreadyRunning) || !strings.Contains(err.Error(), "different workspace") {
		t.Errorf("port held by a server of another workspace: %v", err)
	}
	var se *StartupError
	if !errors.As(err, &se) || !se.OtherWorkspace || se.Port != a.srv.Port() {
		t.Errorf("the failure does not name the port and the other workspace: %#v", err)
	}

	other, lerr := net.Listen("tcp", "127.0.0.1:0")
	if lerr != nil {
		t.Fatal(lerr)
	}
	defer other.Close()
	go func() {
		for {
			c, err := other.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	err = startError(t, func(c *Config) { c.Port = other.Addr().(*net.TCPAddr).Port })
	if !IsStartupCode(err, CodePortInUse) || !errors.As(err, &se) || se.OtherWorkspace || se.Port != other.Addr().(*net.TCPAddr).Port {
		t.Errorf("port held by another program: %#v", err)
	}
}

func TestSecondServerOnSameWorkspaceIsRefused(t *testing.T) {
	h := newHarness(t)
	srv2, err := New(h.config())
	if err != nil {
		t.Fatal(err)
	}
	err = srv2.Start(context.Background())
	if !IsStartupCode(err, CodeServerAlreadyRunning) || !strings.Contains(err.Error(), h.srv.Origin()) {
		t.Fatalf("second server: %v", err)
	}
}

func TestLockReleasedOnShutdownAndStaleLockTakenOver(t *testing.T) {
	h := newHarness(t)
	info, err := ReadLockInfo(h.ws.StateDir())
	if err != nil || info.Port != h.srv.Port() || info.Origin != h.srv.Origin() || info.PathKey != h.srv.pathKey || info.BuildID != "test-build" {
		t.Fatalf("lock document %+v, %v", info, err)
	}
	if held, _ := LockHeld(h.ws.StateDir()); !held {
		t.Fatal("running server does not hold the lock")
	}
	if err := h.srv.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if held, _ := LockHeld(h.ws.StateDir()); held {
		t.Fatal("lock still held after shutdown")
	}
	// The file is left behind, as a killed server would leave it.
	if _, err := ReadLockInfo(h.ws.StateDir()); err != nil {
		t.Fatalf("stale document unreadable: %v", err)
	}
	h.boot()
	h.ready()
	info, _ = ReadLockInfo(h.ws.StateDir())
	if info.Port != h.srv.Port() {
		t.Errorf("document not rewritten: %+v", info)
	}
}

func TestKeychainUnreadableStopsStartupWithoutNewToken(t *testing.T) {
	ws := workspace.Open(t.TempDir())
	mem := vault.NewMemory()
	v := newVaultFor(t, ws, mem)
	if _, err := v.Set(vault.APIToken, "an-existing-token"); err != nil {
		t.Fatal(err)
	}
	mem.FailWith(vault.ErrDenied)
	srv, err := New(Config{Workspace: ws, Vault: v})
	if err != nil {
		t.Fatal(err)
	}
	err = srv.Start(context.Background())
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "keychain") {
		t.Fatalf("want an error naming the keychain, got %v", err)
	}
	mem.FailWith(nil)
	if got, _ := v.Get(vault.APIToken); got != "an-existing-token" {
		t.Fatalf("the token was replaced: %q", got)
	}
	if held, _ := LockHeld(ws.StateDir()); held {
		t.Fatal("failed startup left the lock held")
	}
}

func TestTokenCreatedAtFirstStartAndKept(t *testing.T) {
	h := newHarness(t)
	first := h.token()
	if len(first) != 43 {
		t.Errorf("token has %d characters, want 43 (32 bytes, unpadded base64url)", len(first))
	}
	h.restart()
	if h.token() != first {
		t.Error("the token changed across a restart")
	}
}

func TestTokenFromEnvironmentOverridesKeychain(t *testing.T) {
	ws := workspace.Open(t.TempDir())
	mem := vault.NewMemory()
	id, _ := ws.ID()
	v, _ := vault.New(id, mem, vault.WithGetenv(func(k string) string {
		if k == "APPLYSMITH_SECRET_API_TOKEN" {
			return "token-from-environment-of-at-least-32-bytes"
		}
		return ""
	}))
	srv, err := New(Config{Workspace: ws, Vault: v})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown(context.Background())
	if !srv.token.valid("token-from-environment-of-at-least-32-bytes") {
		t.Error("environment token not accepted")
	}
	if len(mem.Accounts()) != 0 {
		t.Errorf("a keychain entry was created: %v", mem.Accounts())
	}
}

func TestWorkspaceScopedCredentials(t *testing.T) {
	a, b := newHarness(t), newHarness(t)
	tokA := a.token()
	if r := a.do(call{path: "/api/runs/none", bearer: tokA}); r.StatusCode != 404 {
		t.Fatalf("A rejects its own token: %d", r.StatusCode)
	}
	if r := b.do(call{path: "/api/runs/none", bearer: tokA}); r.StatusCode != 401 || r.errCode() != "unauthenticated" {
		t.Fatalf("B accepted A's token: %d %s", r.StatusCode, r.body)
	}
	cookieA := a.session()
	if r := b.do(call{path: "/api/runs/none", cookie: cookieA}); r.StatusCode != 401 {
		t.Fatalf("B accepted A's session: %d", r.StatusCode)
	}
	if a.srv.pathKey == b.srv.pathKey {
		t.Fatal("two workspaces report one identifier")
	}
}

func TestHealthMinimalAndDetailed(t *testing.T) {
	h := newHarness(t)
	r := h.do(call{path: "/api/health"})
	if r.StatusCode != 200 {
		t.Fatalf("status %d", r.StatusCode)
	}
	d := r.data()
	for _, k := range []string{"status", "apiVersion", "version", "buildId", "workspaceId", "startedAt"} {
		if _, ok := d[k]; !ok {
			t.Errorf("minimal health lacks %s", k)
		}
	}
	for _, k := range []string{"components", "uptimeSeconds", "schemaVersion"} {
		if _, ok := d[k]; ok {
			t.Errorf("minimal health leaks %s", k)
		}
	}
	if d["status"] != "ok" || d["buildId"] != "test-build" || d["workspaceId"] != workspace.PathKey(h.ws.Root) || len(d["workspaceId"].(string)) != 12 {
		t.Errorf("health body %v", d)
	}

	h.clock.advance(90 * time.Second)
	d = h.do(call{path: "/api/health", useToken: true}).data()
	comps, _ := d["components"].([]any)
	var names []string
	for _, c := range comps {
		names = append(names, c.(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "database,scheduler,browser,llm,gmail,remote,computer-use" {
		t.Errorf("components %v", names)
	}
	if d["uptimeSeconds"] != float64(90) || d["schemaVersion"] == nil {
		t.Errorf("detailed health %v", d)
	}
	// An invalid credential on a public route is ignored, not an error.
	if r := h.do(call{path: "/api/health", bearer: "wrong"}); r.StatusCode != 200 || r.data()["components"] != nil {
		t.Errorf("invalid credential on health: %d %s", r.StatusCode, r.body)
	}
}

func TestHealthStartingUntilTheDatabaseIsReady(t *testing.T) {
	h := newUnreadyHarness(t)
	if r := h.do(call{path: "/api/health"}); r.data()["status"] != "starting" {
		t.Fatalf("health: %s", r.body)
	}
	for _, c := range []call{{path: "/api/runs/x", useToken: true}, {path: "/"}, {method: "POST", path: "/api/pairing/codes", useToken: true}} {
		if r := h.do(c); r.StatusCode != 503 || r.errCode() != "unavailable" || r.errReason() != wire.ReasonServerStarting {
			t.Errorf("%s while starting: %d %s", c.path, r.StatusCode, r.body)
		}
	}
	h.ready()
	if r := h.do(call{path: "/api/health"}); r.data()["status"] != "ok" {
		t.Fatalf("health after ready: %s", r.body)
	}
}

func TestOrderlyShutdownLetsARequestFinish(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	slow := testRoute("GET", "/api/test/slow", nil, func(c *apikit.Call) (apikit.Reply, error) {
		close(entered)
		<-release
		return apikit.Reply{Data: map[string]string{"done": "yes"}}, nil
	})
	h := newHarness(t, withRoutes(slow))
	var wg sync.WaitGroup
	var res result
	wg.Add(1)
	go func() {
		defer wg.Done()
		res = h.do(call{path: "/api/test/slow", useToken: true})
	}()
	<-entered
	stopped := make(chan error, 1)
	go func() { stopped <- h.srv.Shutdown(context.Background()) }()
	select {
	case <-stopped:
		t.Fatal("shutdown returned while a request was in flight")
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	wg.Wait()
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Errorf("in-flight request got %d", res.StatusCode)
	}
	if held, _ := LockHeld(h.ws.StateDir()); held {
		t.Error("lock held after shutdown")
	}
}

func hasIPv6Loopback() bool {
	ln, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

func TestIPv6HostServesAndPublishesAReachableOrigin(t *testing.T) {
	if !hasIPv6Loopback() {
		t.Skip("no IPv6 loopback")
	}
	h := newHarness(t, func(c *Config) { c.Host = "::1" })
	want := "http://[::1]:" + strconv.Itoa(h.srv.Port())
	if h.srv.Origin() != want {
		t.Fatalf("origin %q, want %q", h.srv.Origin(), want)
	}
	info, err := ReadLockInfo(h.ws.StateDir())
	if err != nil || info.Origin != want {
		t.Fatalf("lock document %+v, %v", info, err)
	}
	if got := h.srv.listener.Addr().String(); !strings.HasPrefix(got, "[::1]:") {
		t.Errorf("listener for host ::1 is %s", got)
	}
	// The published origin must answer.
	if health, ok := ProbeServer(strings.TrimPrefix(info.Origin, "http://")); !ok || health.PathKey != h.srv.pathKey {
		t.Errorf("probe of %s: %+v %v", info.Origin, health, ok)
	}
	if r := h.do(call{path: "/api/health"}); r.StatusCode != 200 {
		t.Errorf("health on %s: %d", want, r.StatusCode)
	}
	// A second server for another workspace on this port is told who holds it.
	err = startError(t, func(c *Config) { c.Host = "::1"; c.Port = h.srv.Port() })
	if !IsStartupCode(err, CodeServerAlreadyRunning) {
		t.Errorf("port held on ::1: %v", err)
	}
}

func TestShortEnvironmentTokenRefusesToStart(t *testing.T) {
	for name, tok := range map[string]string{"one byte": "x", "thirty-one bytes": strings.Repeat("t", 31)} {
		t.Run(name, func(t *testing.T) {
			ws := workspace.Open(t.TempDir())
			mem := vault.NewMemory()
			v := newVaultWithEnv(t, ws, mem, "APPLYSMITH_SECRET_API_TOKEN", tok)
			srv, err := New(Config{Workspace: ws, Vault: v})
			if err != nil {
				t.Fatal(err)
			}
			err = srv.Start(context.Background())
			if err == nil || !strings.Contains(err.Error(), "shorter than 32 bytes") {
				t.Fatalf("start with a short token: %v", err)
			}
			if strings.Contains(err.Error(), tok) && len(tok) > 1 {
				t.Errorf("the error repeats the token: %v", err)
			}
			if held, _ := LockHeld(ws.StateDir()); held {
				t.Error("a refused start kept the lock")
			}
			if len(mem.Accounts()) != 0 {
				t.Errorf("a keychain entry was created: %v", mem.Accounts())
			}
		})
	}
	// Exactly 32 bytes is accepted.
	ws := workspace.Open(t.TempDir())
	v := newVaultWithEnv(t, ws, vault.NewMemory(), "APPLYSMITH_SECRET_API_TOKEN", strings.Repeat("t", 32))
	srv, err := New(Config{Workspace: ws, Vault: v})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("32 bytes refused: %v", err)
	}
	_ = srv.Shutdown(context.Background())
}

// After a forced shutdown the store is gone while a handler may still run; it
// must see an error, not a nil store.
func TestHandlersSeeUnavailableAfterShutdownClosedTheStore(t *testing.T) {
	h := newHarness(t)
	if err := h.srv.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	body := jsonBody(map[string]string{"out": filepath.Join(t.TempDir(), "b")})
	req, _ := http.NewRequest("POST", "/api/backups", nil)
	_, err := h.srv.createBackup(&apikit.Call{R: req, Body: body})
	var ae *apikit.Error
	if !errors.As(err, &ae) || ae.Code != "unavailable" || !ae.Retryable {
		t.Fatalf("createBackup after shutdown: %v", err)
	}
	if _, err := h.srv.db(); err == nil {
		t.Error("db() returned a store after shutdown")
	}
}

func TestShutdownWaitsForARunningBackup(t *testing.T) {
	h := newHarness(t)
	hold, release := make(chan struct{}), make(chan struct{})
	go func() {
		_ = h.srv.store.Load().Write(context.Background(), func(*sql.Tx) error {
			close(hold)
			<-release
			return nil
		})
	}()
	<-hold
	r := h.postBackup(filepath.Join(t.TempDir(), "backup"))
	if r.StatusCode != 202 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	id := r.data()["id"].(string)
	go func() { time.Sleep(100 * time.Millisecond); close(release) }()
	if err := h.srv.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	op, ok := h.srv.backups.get(id)
	if !ok || op.State == "running" {
		t.Errorf("the backup was still running when the store closed: %+v", op)
	}
}

func TestLockDocumentKeepsTheWorkspaceIdName(t *testing.T) {
	h := newHarness(t)
	raw, err := os.ReadFile(LockPath(h.ws.StateDir()))
	if err != nil || !strings.Contains(string(raw), `"workspaceId":"`+workspace.PathKey(h.ws.Root)+`"`) {
		t.Fatalf("lock document: %s, %v", raw, err)
	}
}

// A caller with no credential is shown no components, so its health request
// reads none: it answers at once even while every database reader is taken.
func TestHealthWithoutACredentialReadsNoComponent(t *testing.T) {
	h := newHarness(t)
	h.srv.domain.Load().sched.Wait()
	h.srv.kb.Refresh(t.Context())
	pool := h.srv.store.Load().Read()
	var held []*sql.Conn
	defer func() {
		for _, c := range held {
			c.Close()
		}
	}()
	for range pool.Stats().MaxOpenConnections {
		c, err := pool.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, c)
	}
	start := time.Now()
	r := h.do(call{path: "/api/health"})
	if took := time.Since(start); r.StatusCode != 200 || r.data()["components"] != nil || took >= probeTimeout {
		t.Fatalf("public health with the readers taken: %d in %s, %s", r.StatusCode, took, r.body)
	}
}
