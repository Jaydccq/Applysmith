package cli

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"applysmith/internal/api"
	"applysmith/internal/api/wire"
	"applysmith/internal/vault"
	"applysmith/internal/workspace"
)

// liveServer is the running server of a workspace, ready to be called with the
// bearer token.
type liveServer struct {
	origin string
	token  string
	client *http.Client
}

// serverStartWait bounds how long a command waits for a server that holds the
// workspace lock but is still starting: the lock comes first, then the address,
// then health answers `starting` until the database is migrated and open.
var serverStartWait = 30 * time.Second

// attachLive finds the server that holds the workspace lock. The lock document
// is writable by anyone with the workspace, so the bearer token is read, and
// sent, only after the address proved to be a loopback one that answers as this
// workspace's healthy server. A server that is still starting is waited for,
// with one line on standard error saying so, until ctx ends.
func (e *env) attachLive(ctx context.Context, ws *workspace.Workspace) (*liveServer, error) {
	deadline := time.Now().Add(serverStartWait)
	waiting := false
	for {
		info, err := api.ReadLockInfo(ws.StateDir())
		why := "its address is not recorded yet"
		if err == nil {
			if !api.LoopbackOrigin(info.Origin) {
				return nil, fmt.Errorf("the lock file names %q, which is not a loopback address; refusing to send the API token there", info.Origin)
			}
			u, _ := url.Parse(info.Origin)
			h, ok := api.ProbeServer(u.Host)
			switch {
			case ok && h.PathKey != workspace.PathKey(ws.Root):
				return nil, fmt.Errorf("the server at %s answers for another workspace; refusing to send the API token there", info.Origin)
			case ok && h.Status == wire.HealthOK:
				return e.liveWithToken(ws, info.Origin)
			case ok && h.Status != wire.HealthStarting:
				return nil, fmt.Errorf("the server at %s reports %s; `applysmith doctor --only runtime` says why", info.Origin, h.Status)
			case ok:
				why = "it is opening the workspace database"
			default:
				why = "it does not answer at " + info.Origin + " yet"
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("a server holds this workspace but did not become ready within %s (%s); check it with `applysmith doctor --only runtime`", serverStartWait, why)
		}
		if !waiting {
			waiting = true
			fmt.Fprintf(e.stderr, "waiting up to %s for the server of this workspace to finish starting...\n", serverStartWait)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (e *env) liveWithToken(ws *workspace.Workspace, origin string) (*liveServer, error) {
	v, err := e.vaultOf(ws)
	if err != nil {
		return nil, err
	}
	token, err := v.Get(vault.APIToken)
	if err != nil {
		return nil, fmt.Errorf("cannot read the API token to reach the running server: %w", err)
	}
	return &liveServer{origin: origin, token: token,
		client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (s *liveServer) do(ctx context.Context, method, path string, body []byte, headers ...string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, s.origin+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	return s.client.Do(req)
}

// vaultOf is the workspace's vault; it needs the workspace identifier, which
// the lock document of a running server already implies.
func (e *env) vaultOf(ws *workspace.Workspace) (*vault.Vault, error) {
	id, err := ws.ID()
	if err != nil {
		return nil, err
	}
	return vault.New(id, e.backend, vault.WithGetenv(e.getenv))
}
