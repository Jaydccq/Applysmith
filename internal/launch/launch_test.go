package launch

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"applysmith/internal/api"
	"applysmith/internal/discovery"
	"applysmith/internal/vault"
	"applysmith/internal/workspace"
)

// redeem opens a dashboard address the way a browser would and reports whether
// it was given a session.
func redeem(t *testing.T, openURL string) bool {
	t.Helper()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(openURL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	for _, c := range resp.Cookies() {
		if c.Name == api.SessionCookie {
			return true
		}
	}
	return false
}

func opts(ws *workspace.Workspace) Options {
	return Options{
		Workspace: ws, Backend: vault.NewMemory(), Getenv: func(string) string { return "" },
		Version: "test", BuildID: "test",
	}
}

func TestPortFromEnv(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		want    int
		wantErr bool
	}{
		{"", 47319, false}, {"0", 0, false}, {"8123", 8123, false}, {"70000", 0, true}, {"abc", 0, true},
	} {
		got, err := PortFromEnv(func(string) string { return tc.raw })
		if (err != nil) != tc.wantErr || (err == nil && got != tc.want) {
			t.Errorf("PortFromEnv(%q) = %d, %v", tc.raw, got, err)
		}
	}
}

// Scenario "Attach to a running server" and "Start in-process" of the
// desktop-shell capability: no holder means start, a healthy holder means the
// origin of that holder, and releasing the lock makes the workspace free again.
func TestAttachFindsHealthyServerAndNoneOtherwise(t *testing.T) {
	ws := workspace.Open(filepath.Join(t.TempDir(), "ws"))
	ctx := context.Background()

	if run, err := Attach(ctx, ws, time.Second); err != nil || run != nil {
		t.Fatalf("empty workspace: %+v, err %v", run, err)
	}

	srv, err := Start(ctx, opts(ws))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ws.Path(workspace.FileAnswerBank)); err != nil {
		t.Fatalf("a started server leaves the workspace without its default answer bank: %v", err)
	}
	run, err := Attach(ctx, ws, 5*time.Second)
	if err != nil || run == nil || run.Origin != srv.Origin() {
		t.Fatalf("attach: %+v (want origin %q), err %v", run, srv.Origin(), err)
	}
	if _, err := Start(ctx, opts(ws)); err == nil {
		t.Fatal("a second server started for a workspace that has one")
	}

	if err := srv.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if run, err := Attach(ctx, ws, time.Second); err != nil || run != nil {
		t.Fatalf("after shutdown: %+v, err %v", run, err)
	}
	srv2, err := Start(ctx, opts(ws))
	if err != nil {
		t.Fatalf("restart after shutdown: %v", err)
	}
	_ = srv2.Shutdown(ctx)
}

func TestAttachHandsOverACodeThatOpensTheDashboard(t *testing.T) {
	ws := workspace.Open(filepath.Join(t.TempDir(), "ws"))
	ctx := context.Background()
	srv, err := Start(ctx, opts(ws))
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown(ctx)
	run, err := Attach(ctx, ws, 5*time.Second)
	if err != nil || run.LaunchCode == "" {
		t.Fatalf("%+v, %v", run, err)
	}
	if !redeem(t, api.DashboardURL(run.Origin, run.LaunchCode)) {
		t.Error("the code from the lock document did not open the dashboard")
	}
	if redeem(t, api.DashboardURL(run.Origin, run.LaunchCode)) {
		t.Error("the code worked twice")
	}
}

// The lock file can be edited by anyone with the workspace; an address that is
// not this machine is never connected to.
func TestAttachRefusesANonLoopbackOrigin(t *testing.T) {
	ws := workspace.Open(filepath.Join(t.TempDir(), "ws"))
	lock, err := api.AcquireLock(ws.StateDir())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	info := api.LockInfo{PID: 1, Port: 80, PathKey: workspace.PathKey(ws.Root), Origin: "http://203.0.113.9:80"}
	if err := lock.WriteInfo(info); err != nil {
		t.Fatal(err)
	}
	run, err := Attach(context.Background(), ws, 50*time.Millisecond)
	if !errors.Is(err, ErrHolderUnhealthy) || !strings.Contains(err.Error(), "not a loopback address") || run != nil {
		t.Fatalf("%+v, %v", run, err)
	}
}

// The origin comes from the lock document, not from an assumed 127.0.0.1.
func TestAttachProbesTheAddressTheServerPublished(t *testing.T) {
	if ln, err := net.Listen("tcp6", "[::1]:0"); err != nil {
		t.Skip("no IPv6 loopback")
	} else {
		ln.Close()
	}
	ws := workspace.Open(filepath.Join(t.TempDir(), "ws"))
	ctx := context.Background()
	o := opts(ws)
	o.Host = "::1"
	srv, err := Start(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown(ctx)
	run, err := Attach(ctx, ws, 5*time.Second)
	if err != nil || run == nil || run.Origin != srv.Origin() {
		t.Fatalf("%+v, %v", run, err)
	}
}

func TestAttachOrStartStartsThenAttaches(t *testing.T) {
	ws := workspace.Open(filepath.Join(t.TempDir(), "ws"))
	ctx := context.Background()
	first, err := AttachOrStart(ctx, opts(ws), 5*time.Second)
	if err != nil || first.Server == nil {
		t.Fatalf("%+v, %v", first, err)
	}
	defer first.Server.Shutdown(ctx)
	second, err := AttachOrStart(ctx, opts(ws), 5*time.Second)
	if err != nil || second.Server != nil || second.Origin != first.Origin {
		t.Fatalf("%+v, %v", second, err)
	}
	// Both addresses carry the one live code, which works once.
	if first.OpenURL != second.OpenURL || !redeem(t, second.OpenURL) || redeem(t, first.OpenURL) {
		t.Error("an address handed out by AttachOrStart does not open the dashboard exactly once")
	}
	third, err := AttachOrStart(ctx, opts(ws), 5*time.Second)
	if err != nil || !redeem(t, third.OpenURL) {
		t.Errorf("the replacement code does not open the dashboard: %+v, %v", third, err)
	}
}

// Another process can take the lock between the look and the start; the app
// then attaches to the winner.
func TestAttachOrStartAttachesWhenItLosesTheStartRace(t *testing.T) {
	ws := workspace.Open(filepath.Join(t.TempDir(), "ws"))
	ctx := context.Background()
	var winner *api.Server
	real := startServer
	startServer = func(ctx context.Context, o Options) (*api.Server, error) {
		var err error
		if winner, err = real(ctx, o); err != nil {
			return nil, err
		}
		// This caller's own attempt now finds the lock taken.
		return real(ctx, o)
	}
	defer func() { startServer = real }()
	sess, err := AttachOrStart(ctx, opts(ws), 5*time.Second)
	if winner != nil {
		defer winner.Shutdown(ctx)
	}
	if err != nil || winner == nil || sess.Server != nil || sess.Origin != winner.Origin() {
		t.Fatalf("%+v, %v", sess, err)
	}
}

// A code read from the lock document may be about to expire, because the
// server keeps an unused code until its end. Attaching asks the server for a
// fresh one so the window never opens with a link that dies at once.
func TestAttachOrStartAsksTheRunningServerForAFreshCode(t *testing.T) {
	ws := workspace.Open(filepath.Join(t.TempDir(), "ws"))
	ctx := context.Background()
	o := opts(ws)
	srv, err := Start(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown(ctx)
	before, err := api.ReadLockInfo(ws.StateDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := AttachOrStart(ctx, o, 5*time.Second)
	if err != nil || s.Server != nil {
		t.Fatalf("%+v, %v", s, err)
	}
	if strings.Contains(s.OpenURL, before.LaunchCode) {
		t.Error("the window was given the code that was already in the lock document")
	}
	if !redeem(t, s.OpenURL) {
		t.Error("the fresh code did not open the dashboard")
	}
}

func TestAttachOrStartFallsBackToTheLockDocumentCode(t *testing.T) {
	ws := workspace.Open(filepath.Join(t.TempDir(), "ws"))
	ctx := context.Background()
	mem := vault.NewMemory()
	o := opts(ws)
	o.Backend = mem
	srv, err := Start(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown(ctx)
	mem.FailWith(vault.ErrDenied) // the token cannot be read
	s, err := AttachOrStart(ctx, o, 5*time.Second)
	mem.FailWith(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !redeem(t, s.OpenURL) {
		t.Error("the lock document code did not open the dashboard")
	}
}

// A port held by a server of another workspace is the cause the app reports;
// it is not a lost race for the lock, so it is not retried into a vaguer error.
func TestAttachOrStartReportsAPortHeldByAnotherWorkspaceAsIs(t *testing.T) {
	other := workspace.Open(filepath.Join(t.TempDir(), "other"))
	holder, err := Start(context.Background(), opts(other))
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Shutdown(context.Background())

	ws := workspace.Open(filepath.Join(t.TempDir(), "ws"))
	o := opts(ws)
	o.Port = holder.Port()
	_, err = AttachOrStart(context.Background(), o, time.Second)
	var se *api.StartupError
	if !errors.As(err, &se) || !se.OtherWorkspace || se.Port != holder.Port() || !api.IsStartupCode(err, api.CodeServerAlreadyRunning) {
		t.Fatalf("err = %#v", err)
	}
}

// The desktop starts on any free port, and the browser extension keeps the
// address it was paired with: a restart asks for the last port again, takes
// another when that one is held, and never takes the CLI default.
func TestStartReusesTheLastPortWhenItIsFree(t *testing.T) {
	ws := workspace.Open(filepath.Join(t.TempDir(), "ws"))
	ctx := context.Background()
	o := opts(ws)
	o.ReuseLastPort = true
	port := func(s *api.Server) string { return s.Origin()[strings.LastIndex(s.Origin(), ":")+1:] }

	first, err := Start(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	was := port(first)
	_ = first.Shutdown(ctx)
	second, err := Start(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if last := port(second); last != was {
		// Another process on this shared machine may take the freed port in
		// the gap; the restart is wrong only if the port is free.
		if l, err := net.Listen("tcp", "127.0.0.1:"+was); err == nil {
			_ = l.Close()
			t.Fatalf("restart listens on %s, want the last port %s", last, was)
		}
		was = last
	}
	_ = second.Shutdown(ctx)

	holder, err := net.Listen("tcp", "127.0.0.1:"+was)
	if err != nil {
		t.Fatal(err)
	}
	third, err := Start(ctx, o)
	_ = holder.Close()
	if err != nil {
		t.Fatalf("a held last port must fall back to a free one: %v", err)
	}
	if port(third) == was {
		t.Fatalf("listens on the held port %s", was)
	}
	_ = third.Shutdown(ctx)

	without := opts(workspace.Open(filepath.Join(t.TempDir(), "other")))
	a, err := Start(ctx, without)
	if err != nil {
		t.Fatal(err)
	}
	_ = a.Shutdown(ctx)
	if info, err := api.ReadLockInfo(without.Workspace.StateDir()); err != nil || info.Port == 0 {
		t.Fatalf("lock document after shutdown: %+v, %v", info, err)
	}
}

// Switching workspaces in the desktop app must not put the new server on the
// address the extension was paired to in the old one: it would send a token
// that server rejects, and the extension drops a rejected pairing.
func TestStartNeverTakesTheAvoidedPort(t *testing.T) {
	ws := workspace.Open(filepath.Join(t.TempDir(), "ws"))
	ctx := context.Background()
	o := opts(ws)
	o.ReuseLastPort = true
	first, err := Start(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	was := first.Origin()
	_ = first.Shutdown(ctx)
	o.AvoidPort, _ = strconv.Atoi(was[strings.LastIndex(was, ":")+1:])
	second, err := Start(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Shutdown(ctx)
	if second.Origin() == was {
		t.Fatalf("the server took the avoided address %s", was)
	}
}

// LinkedIn holds a reader back with HTTP 999 and a script that sends the
// browser on to its sign-in wall. The read is of where the page ended, and it
// remembers the throttle it met (flow round 10); a page that met none is not
// throttled.
func TestLiveness_AThrottleMetOnTheWayIsRemembered(t *testing.T) {
	pg := discovery.PageRead{Status: 999}
	livenessDoc{Href: "https://www.linkedin.com/authwall?trk=bf", Status: 200}.fill(&pg)
	if pg.Status != 200 || pg.FinalURL != "https://www.linkedin.com/authwall?trk=bf" || !pg.Throttled {
		t.Fatalf("read = %+v", pg)
	}
	pg = discovery.PageRead{Status: 200}
	livenessDoc{Status: 404}.fill(&pg)
	if pg.Throttled || pg.Status != 404 {
		t.Fatalf("read of a page that met no throttle = %+v", pg)
	}
}
