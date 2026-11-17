// Package launch starts the server for a workspace and finds one that already
// runs. The headless command and the desktop app share it, so both take the
// workspace lock in the same order and neither can start a second server.
package launch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"time"

	"applysmith/internal/answerbank"
	"applysmith/internal/api"
	"applysmith/internal/api/wire"
	"applysmith/internal/browser/browserimport"
	"applysmith/internal/computeruse"
	"applysmith/internal/onboarding"
	"applysmith/internal/remote/tunnel"
	"applysmith/internal/store"
	"applysmith/internal/vault"
	"applysmith/internal/workspace"
)

// Options describes the server to start.
type Options struct {
	Workspace *workspace.Workspace
	Backend   vault.Backend
	Getenv    func(string) string
	Version   string
	BuildID   string
	Host      string
	Port      int
	Assets    fs.FS // embedded dashboard, nil serves a placeholder page

	// ReuseLastPort asks, when Port is 0, for the port the workspace's last
	// server listened on, so the paired browser extension, which cannot read
	// the workspace lock, finds the application at the same address after a
	// restart. A port that is now taken falls back to a free one; the CLI
	// default is never reused, because it belongs to the CLI and the original
	// application.
	ReuseLastPort bool
	// AvoidPort is the port the server never takes (api.Config.AvoidPort).
	AvoidPort int
	// Domain replaces the model providers, posting reader and discovery
	// endpoints; tests use it, the zero value is the product.
	Domain api.DomainOptions
	// RemoteTunnel replaces the supervisor of the tunnel child (tests).
	RemoteTunnel func(tunnel.Config) api.TunnelProcess
	// RemoteEvery replaces how often policy.yml is re-read (tests).
	RemoteEvery time.Duration
}

// SnapshotDir is where the scheduler's daily snapshots of a workspace go: a
// sibling directory named after the workspace, outside it, so a snapshot never
// holds another snapshot.
func SnapshotDir(ws *workspace.Workspace) string {
	return filepath.Join(filepath.Dir(ws.Root), filepath.Base(ws.Root)+"-backups")
}

// PortFromEnv is APPLYSMITH_PORT when set, else the product default.
func PortFromEnv(getenv func(string) string) (int, error) {
	raw := getenv("APPLYSMITH_PORT")
	if raw == "" {
		return api.DefaultPort, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 || n > 65535 {
		return 0, fmt.Errorf("APPLYSMITH_PORT %q is not a port number", raw)
	}
	return n, nil
}

// Start runs the server until Shutdown. The order matters: the lock first, so
// nothing touches the workspace database without it; the listener next, so
// health answers `starting` during migrations; the store last.
func Start(ctx context.Context, o Options) (*api.Server, error) {
	preferred := 0
	if o.Port == 0 && o.ReuseLastPort {
		// Read before the lock is taken: taking it clears the last document.
		if last, err := api.ReadLockInfo(o.Workspace.StateDir()); err == nil && last.Port != api.DefaultPort {
			preferred = last.Port
		}
	}
	lock, err := api.AcquireLock(o.Workspace.StateDir())
	if err != nil {
		return nil, err
	}
	id, err := o.Workspace.ID()
	if err != nil {
		_ = lock.Release()
		return nil, err
	}
	v, err := vault.New(id, o.Backend, vault.WithGetenv(o.Getenv))
	if err != nil {
		_ = lock.Release()
		return nil, err
	}
	domain := o.Domain
	if domain.Liveness == nil {
		domain.Liveness = LivenessReader(o.Workspace)
	}
	if domain.PiPath == "" {
		domain.PiPath = o.Getenv("APPLYSMITH_PI_PATH")
	}
	deps := &sessionDeps{vault: v}
	if domain.ScanWindow == nil {
		domain.ScanWindow = ScanWindow(o.Workspace, deps.watchScans(o.Workspace))
	}
	srv, err := api.New(api.Config{
		Workspace: o.Workspace, Vault: v, Version: o.Version, BuildID: o.BuildID,
		Host: o.Host, Port: o.Port, PreferredPort: preferred, AvoidPort: o.AvoidPort, Assets: o.Assets, LogLevel: o.Getenv("APPLYSMITH_LOG_LEVEL"), Lock: lock, Domain: domain,
		SnapshotDir:    SnapshotDir(o.Workspace),
		SessionBrowser: sessionBrowser(o.Workspace, deps), CookieImport: browserimport.Reader{}, ApplyWindow: applyWindow(o.Workspace), OpenInApplyWindow: openInApplyWindow(o.Workspace),
		BrowserControl: browserControl(o.Workspace), ChromeCheck: chromeCheck(), ComputerUse: computeruse.Open(computeruse.RuntimeDir(o.Getenv)),
		ManagedBrowserPID: managedBrowserPID(o.Workspace),
		Setup:             &onboarding.Service{Backend: o.Backend, Getenv: o.Getenv, BrowserCheck: BrowserCheck(o.Workspace)},
		RemoteTunnel:      o.RemoteTunnel, RemoteEvery: o.RemoteEvery,
	})
	if err != nil {
		_ = lock.Release()
		return nil, err
	}
	if err := srv.Start(ctx); err != nil {
		return nil, err
	}
	st, err := openStore(ctx, o.Workspace, id)
	if err != nil {
		_ = srv.Shutdown(context.Background())
		return nil, err
	}
	if _, err := answerbank.New(o.Workspace, st).Seed(); err != nil {
		_ = st.Close()
		_ = srv.Shutdown(context.Background())
		return nil, fmt.Errorf("seed the answer bank: %w", err)
	}
	deps.st.Store(st)
	if err := srv.Ready(st); err != nil {
		_ = srv.Shutdown(context.Background())
		return nil, err
	}
	return srv, nil
}

// openStore migrates the database and finishes interrupted report commits.
func openStore(ctx context.Context, ws *workspace.Workspace, id string) (*store.Store, error) {
	st, err := store.Open(ctx, filepath.Join(ws.StateDir(), store.DBFile), store.Options{WorkspaceID: id})
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := st.RecoverArtifacts(ctx, ws.Root); err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("recover report artifacts: %w", err)
	}
	return st, nil
}

// Running is a server that already holds the workspace lock.
type Running struct {
	Origin string
	// LaunchCode is the one-time code that opens the dashboard, empty when the
	// server's lock document carries none.
	LaunchCode string
}

// ErrHolderUnhealthy means a process holds the workspace lock but no healthy
// server answers for it. A second server is never started beside it.
var ErrHolderUnhealthy = errors.New("a server holds the workspace lock but is not healthy")

// Attach looks for a server that already holds the workspace lock. It returns
// the server of this workspace whose health is `ok`, waiting up to wait for one
// that is still starting, and nil when no server holds the lock. A holder that
// never becomes healthy is an error, never a reason to start a second server.
// The lock document names the address to connect to, so it is believed only
// when it is a loopback one.
func Attach(ctx context.Context, ws *workspace.Workspace, wait time.Duration) (*Running, error) {
	held, err := api.LockHeld(ws.StateDir())
	if err != nil || !held {
		return nil, err
	}
	want := workspace.PathKey(ws.Root)
	deadline := time.Now().Add(wait)
	for {
		if info, err := api.ReadLockInfo(ws.StateDir()); err == nil && info.PathKey == want {
			if !api.LoopbackOrigin(info.Origin) {
				return nil, fmt.Errorf("%w: the lock file names %q, which is not a loopback address", ErrHolderUnhealthy, info.Origin)
			}
			u, _ := url.Parse(info.Origin)
			if h, ok := api.ProbeServer(u.Host); ok && h.PathKey == want && h.Status == "ok" {
				return &Running{Origin: info.Origin, LaunchCode: info.LaunchCode}, nil
			}
		}
		if time.Now().After(deadline) {
			return nil, ErrHolderUnhealthy
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// mintLaunchCode asks the attached server for a fresh launch code with the
// bearer token. The code in the lock document is whatever the server held when
// it last looked and may be nearly expired; a failure here only means that
// document is used as it is.
func mintLaunchCode(ctx context.Context, o Options, origin string) (string, error) {
	id, err := o.Workspace.ID()
	if err != nil {
		return "", err
	}
	v, err := vault.New(id, o.Backend, vault.WithGetenv(o.Getenv))
	if err != nil {
		return "", err
	}
	token, err := v.Get(vault.APIToken)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, mintTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/api/launch-codes", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var env wire.Success[wire.LaunchCode]
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&env) != nil || env.Data.Code == "" {
		return "", fmt.Errorf("the server did not mint a launch code (status %d)", resp.StatusCode)
	}
	return env.Data.Code, nil
}

const mintTimeout = 3 * time.Second

// Session is what a front end needs to open the dashboard.
type Session struct {
	Server  *api.Server // set only when this process started the server
	Origin  string
	OpenURL string // opens the dashboard once
}

// startServer is Start; tests replace it to stage a lost race.
var startServer = Start

// maxAttachRounds bounds the retry when another process wins the start race.
const maxAttachRounds = 3

// AttachOrStart uses the server that runs for the workspace, or starts one.
// When another process takes the lock between the look and the start, the look
// is repeated: the winner is the server to use, not a failure.
func AttachOrStart(ctx context.Context, o Options, wait time.Duration) (*Session, error) {
	for range maxAttachRounds {
		run, err := Attach(ctx, o.Workspace, wait)
		if err != nil {
			return nil, err
		}
		if run != nil {
			code := run.LaunchCode
			if fresh, err := mintLaunchCode(ctx, o, run.Origin); err == nil {
				code = fresh
			}
			return &Session{Origin: run.Origin, OpenURL: api.DashboardURL(run.Origin, code)}, nil
		}
		srv, err := startServer(ctx, o)
		// Only a lost race for the lock is repeated; a port held by a server
		// of another workspace is the answer.
		var se *api.StartupError
		if api.IsStartupCode(err, api.CodeServerAlreadyRunning) && !(errors.As(err, &se) && se.OtherWorkspace) {
			continue
		}
		if err != nil {
			return nil, err
		}
		open, err := srv.LaunchURL()
		if err != nil {
			_ = srv.Shutdown(context.Background())
			return nil, err
		}
		return &Session{Server: srv, Origin: srv.Origin(), OpenURL: open}, nil
	}
	return nil, errors.New("another process keeps taking the workspace lock; try again")
}

// VCSRevision is the commit the Go toolchain recorded, empty when unknown.
func VCSRevision() (rev string, dirty bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	return rev, dirty
}

// BuildID is the identifier health reports: the commit, or "dev" when the
// binary was not built from a repository.
func BuildID() string {
	if rev, _ := VCSRevision(); rev != "" {
		return rev
	}
	return "dev"
}
