package launch

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"applysmith/internal/answerbank"
	"applysmith/internal/api"
	"applysmith/internal/apply"
	"applysmith/internal/browser"
	"applysmith/internal/credentials"
	"applysmith/internal/store"
	"applysmith/internal/vault"
	"applysmith/internal/workspace"
)

var _ api.SessionBrowser = (*browser.Sessions)(nil)

// browserOptions describe the managed browser of a workspace. Its profile and
// the undo snapshot live under the workspace's browser directory; nothing is
// created and no browser starts until the first use.
func browserOptions(ws *workspace.Workspace) (browser.Options, string) {
	dir := filepath.Join(ws.StateDir(), "browser")
	return browser.Options{
		ProfileDir:   filepath.Join(dir, "profile"),
		StateDir:     filepath.Join(dir, "state"),
		DownloadsDir: filepath.Join(ws.StateDir(), "downloads"),
		EventLogPath: filepath.Join(ws.StateDir(), "logs", "browser-events.jsonl"),
		Environ:      os.Environ(),
	}, dir
}

// sessionDeps are the pieces of the sign-in sync that need the database and
// the vault. The sync is built before the database is open, so they arrive
// later; until then the sync lists no stored accounts and no probe results.
type sessionDeps struct {
	st    atomic.Pointer[store.Store]
	vault *vault.Vault
}

// watchScans reads the "watch scans" runtime setting. Before the store is
// open, and on any read problem, scans stay hidden.
func (d *sessionDeps) watchScans(ws *workspace.Workspace) func(context.Context) bool {
	return func(ctx context.Context) bool {
		st := d.st.Load()
		return st != nil && answerbank.New(ws, st).Settings(ctx).WatchScans
	}
}

func (d *sessionDeps) accounts(ctx context.Context) ([]browser.StoredAccount, error) {
	st := d.st.Load()
	if st == nil {
		return nil, nil
	}
	list, err := credentials.New(st, d.vault, nil).StoredAccounts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]browser.StoredAccount, len(list))
	for i, a := range list {
		out[i] = browser.StoredAccount{Host: a.Host, System: a.System, Tenant: a.Tenant}
	}
	return out, nil
}

// signInStates keeps the sign-in probe's last result in the workspace
// database.
type signInStates struct{ d *sessionDeps }

const probeStamp = "2006-01-02T15:04:05.000Z"

func (p signInStates) Last(ctx context.Context) (map[string]browser.SignInRecord, error) {
	st := p.d.st.Load()
	if st == nil {
		return nil, nil
	}
	return ReadSignInStates(ctx, st)
}

func (p signInStates) Save(ctx context.Context, domain string, state browser.SignInState, at time.Time) error {
	st := p.d.st.Load()
	if st == nil {
		return errors.New("launch: the database is not open yet")
	}
	return SaveSignInState(ctx, st, domain, state, at)
}

// ReadSignInStates lists the stored probe results by site domain.
func ReadSignInStates(ctx context.Context, st *store.Store) (map[string]browser.SignInRecord, error) {
	rows, err := st.Read().QueryContext(ctx, `SELECT domain, state, checked_at FROM signin_probes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]browser.SignInRecord{}
	for rows.Next() {
		var domain, state, at string
		if err := rows.Scan(&domain, &state, &at); err != nil {
			return nil, err
		}
		t, err := time.Parse(probeStamp, at)
		if err != nil {
			continue
		}
		out[domain] = browser.SignInRecord{State: browser.SignInState(state), CheckedAt: t}
	}
	return out, rows.Err()
}

// SaveSignInState stores the probe result of one site.
func SaveSignInState(ctx context.Context, st *store.Store, domain string, state browser.SignInState, at time.Time) error {
	return st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO signin_probes (domain, state, checked_at) VALUES (?, ?, ?)
			ON CONFLICT(domain) DO UPDATE SET state = excluded.state, checked_at = excluded.checked_at`,
			domain, string(state), at.UTC().Format(probeStamp))
		return err
	})
}

// sessionBrowser is the managed browser as the sign-in sync sees it.
func sessionBrowser(ws *workspace.Workspace, d *sessionDeps) api.SessionBrowser {
	o, dir := browserOptions(ws)
	return browser.InstanceSessions(o, dir, d.accounts, signInStates{d})
}

// ProbeSessions returns the sign-in probe over an open database, for the
// command line. The browser is started or adopted by the first probe.
func ProbeSessions(ws *workspace.Workspace, st *store.Store) *browser.Sessions {
	d := &sessionDeps{}
	d.st.Store(st)
	o, dir := browserOptions(ws)
	return browser.InstanceSessions(o, dir, nil, signInStates{d})
}

// applyWindow attaches the application chain to the apply lane of the managed
// browser, starting or adopting it on first use. Concurrent attaches share the
// one instance.
func applyWindow(ws *workspace.Workspace) func(context.Context) (apply.Window, error) {
	o, _ := browserOptions(ws)
	var mu sync.Mutex
	return func(ctx context.Context) (apply.Window, error) {
		mu.Lock()
		if o.Chrome.Path == "" {
			c, err := browser.DiscoverChrome(ctx, os.Getenv)
			if err != nil {
				mu.Unlock()
				return nil, err
			}
			o.Chrome = c
		}
		opts := o
		mu.Unlock()
		inst, err := browser.Open(ctx, opts)
		if err != nil {
			return nil, err
		}
		return apply.LaneWindow{Lane: inst.Lane(browser.LaneApply), Leased: true}, nil
	}
}

// openInApplyWindow opens a page in a tab the user owns in the apply window and
// brings the window forward. It is the one place a window is raised, reached
// only from the dashboard route.
func openInApplyWindow(ws *workspace.Workspace) func(context.Context, string) error {
	attach := applyWindow(ws)
	return func(ctx context.Context, rawURL string) error {
		w, err := attach(ctx)
		if err != nil {
			return err
		}
		lane := w.(apply.LaneWindow).Lane
		id, err := lane.OpenTab(ctx, "")
		if err != nil {
			return err
		}
		if _, err := lane.Navigate(ctx, id, rawURL); err != nil {
			_ = lane.CloseTab(context.WithoutCancel(ctx), id)
			return err
		}
		if err := lane.ShowWindow(ctx, id); err != nil {
			return err
		}
		return lane.Keep(id)
	}
}

// managedBrowserPID is the process id of the workspace's managed browser while
// this process has it open.
func managedBrowserPID(ws *workspace.Workspace) func() int {
	o, _ := browserOptions(ws)
	return func() int { return browser.ManagedPID(o.ProfileDir) }
}

// browserControl serves the Browser page's status, screenshots and window
// controls from the managed browser of the workspace.
func browserControl(ws *workspace.Workspace) api.BrowserControl {
	o, _ := browserOptions(ws)
	return browser.NewControl(o)
}

// chromeCheck reports whether Google Chrome can be found. A found Chrome is
// remembered; a missing one is looked for again at each call, so installing it
// takes effect without a restart.
func chromeCheck() func(context.Context) error {
	var found atomic.Bool
	return func(ctx context.Context) error {
		if found.Load() {
			return nil
		}
		if _, err := browser.DiscoverChrome(ctx, os.Getenv); err != nil {
			return err
		}
		found.Store(true)
		return nil
	}
}
