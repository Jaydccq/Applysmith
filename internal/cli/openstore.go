package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"applysmith/internal/api"
	"applysmith/internal/store"
	"applysmith/internal/workspace"
)

// storeAccess says how a command uses the workspace database.
type storeAccess int

const (
	// accessRead opens the database read-only: no lock, no write, no recovery.
	accessRead storeAccess = iota
	// accessShared joins the database as one more writer beside a running
	// server (WAL, busy timeout, no lock file). Only a database that needs
	// migrating takes the exclusive path, for that command alone.
	accessShared
	// accessExclusive holds the workspace lock for the whole command. Commands
	// that run an in-process worker pool (evaluate, scan, the knowledge base)
	// assume nothing else works on the workspace.
	accessExclusive
)

var errNoDatabase = errors.New("this workspace has no database yet; `applysmith init` creates it")

// openStore is the one way a CLI command opens the workspace database. A
// writable open settles the publish journals of a crashed process (see
// store.OpenWorkspace) and returns a release function that closes the store
// and drops the lock if one was taken.
func (e *env) openStore(ctx context.Context, root string, access storeAccess) (*workspace.Workspace, *store.Store, func(), error) {
	ws := workspace.Open(root)
	if access == accessRead {
		st, err := store.OpenReadOnly(ctx, filepath.Join(ws.StateDir(), store.DBFile))
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil, errNoDatabase
		}
		if err != nil {
			return nil, nil, nil, err
		}
		return ws, st, func() { _ = st.Close() }, nil
	}
	id, err := ws.ID()
	if err != nil {
		return nil, nil, nil, err
	}
	if access == accessShared {
		st, err := store.OpenWorkspace(ctx, root, store.Options{WorkspaceID: id, Shared: true})
		if !errors.Is(err, store.ErrMigrationPending) {
			if err != nil {
				return nil, nil, nil, err
			}
			return ws, st, func() { _ = st.Close() }, nil
		}
	}
	lock, err := api.AcquireLock(ws.StateDir())
	if api.IsStartupCode(err, api.CodeServerAlreadyRunning) || api.IsStartupCode(err, api.CodeWorkspaceLocked) {
		return nil, nil, nil, fmt.Errorf("%w; use the dashboard, or stop that process and run the command again", err)
	}
	if err != nil {
		return nil, nil, nil, err
	}
	st, err := store.OpenWorkspace(ctx, root, store.Options{WorkspaceID: id})
	if err != nil {
		lock.Release()
		return nil, nil, nil, err
	}
	return ws, st, func() { _ = st.Close(); lock.Release() }, nil
}
