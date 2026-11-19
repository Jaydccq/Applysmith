package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"slices"
	"time"

	"applysmith/internal/api"
	"applysmith/internal/api/wire"
	"applysmith/internal/vault"
	"applysmith/internal/workspace"
)

const tokenUsage = `usage:
  applysmith token rotate [--workspace <dir>]   replace the API token and print the new one
`

// liveCallTimeout bounds one request to the running server.
var liveCallTimeout = 10 * time.Second

// pair prints the bearer token for the CLI user. It reads the vault and never
// creates a token: the server makes it at its first start.
func (e *env) pair(args []string) int {
	fs := e.flags("pair")
	wsFlag := fs.String("workspace", "", "workspace directory")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(e.stderr, "applysmith pair: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	ws, err := e.openWorkspace(*wsFlag)
	if err != nil {
		return e.fail("pair", err)
	}
	if _, ok, err := ws.ExistingID(); err != nil {
		return e.fail("pair", err)
	} else if !ok {
		return e.fail("pair", errNoToken)
	}
	v, err := e.vaultOf(ws)
	if err != nil {
		return e.fail("pair", err)
	}
	token, err := v.Get(vault.APIToken)
	switch {
	case vault.IsCode(err, vault.CodeNotFound):
		return e.fail("pair", errNoToken)
	case err != nil:
		return e.fail("pair", err)
	}
	fmt.Fprintln(e.stdout, token)
	fmt.Fprintln(e.stderr, "Paste this token into the extension popup. It is shown here only; `applysmith token rotate` replaces it.")
	return 0
}

var errNoToken = errors.New("this workspace has no API token yet; it is created when `applysmith serve` (or the app) first starts")

// token runs the token subcommands.
func (e *env) token(ctx context.Context, args []string) int {
	if len(args) == 0 || args[0] != "rotate" {
		fmt.Fprint(e.stderr, tokenUsage)
		return 2
	}
	fs := e.flags("token rotate")
	wsFlag := fs.String("workspace", "", "workspace directory")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 {
		fmt.Fprint(e.stderr, tokenUsage)
		return 2
	}
	ws, err := e.openWorkspace(*wsFlag)
	if err != nil {
		return e.fail("token", err)
	}
	lock, err := api.AcquireLock(ws.StateDir())
	switch {
	case err == nil:
		defer lock.Release()
		return e.rotateOffline(ws)
	case api.IsStartupCode(err, api.CodeServerAlreadyRunning):
		return e.rotateLive(ctx, ws)
	}
	return e.fail("token", err)
}

// rotateOffline changes the stored token while no server runs; holding the
// workspace lock keeps one from starting in the middle.
func (e *env) rotateOffline(ws *workspace.Workspace) int {
	v, err := e.vaultOf(ws)
	if err != nil {
		return e.fail("token", err)
	}
	tok, err := api.RotateStoredToken(v)
	if errors.Is(err, api.ErrEnvManaged) {
		return e.fail("token", envManagedErr)
	}
	if err != nil {
		return e.fail("token", err)
	}
	fmt.Fprintln(e.stdout, tok)
	return 0
}

var envManagedErr = errors.New("the API token is supplied by APPLYSMITH_SECRET_API_TOKEN; change it there")

// rotateLive asks the running server, which also closes the streams opened
// with the old token.
func (e *env) rotateLive(ctx context.Context, ws *workspace.Workspace) int {
	srv, err := e.attachLive(ctx, ws)
	if err != nil {
		return e.fail("token", err)
	}
	var issued wire.TokenIssued
	if err := e.liveCall(ctx, srv, http.MethodPost, "/api/token/rotate", &issued); err != nil {
		return e.fail("token", err)
	}
	fmt.Fprintln(e.stdout, issued.Token)
	return 0
}

// open prints a fresh dashboard link, minted by the running server. The link
// goes to the standard output only unless --browser is given: handing it to
// the system opener puts it on a command line other users of the machine can
// list for an instant.
func (e *env) open(ctx context.Context, args []string) int {
	fs := e.flags("open")
	wsFlag := fs.String("workspace", "", "workspace directory")
	browser := fs.Bool("browser", false, "also open the link in the default browser")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(e.stderr, "applysmith open: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	ws, err := e.openWorkspace(*wsFlag)
	if err != nil {
		return e.fail("open", err)
	}
	held, err := api.LockHeld(ws.StateDir())
	if err != nil {
		return e.fail("open", err)
	}
	if !held {
		return e.fail("open", errNoServer)
	}
	srv, err := e.attachLive(ctx, ws)
	if err != nil {
		return e.fail("open", err)
	}
	var code wire.LaunchCode
	if err := e.liveCall(ctx, srv, http.MethodPost, "/api/launch-codes", &code); err != nil {
		return e.fail("open", err)
	}
	link := api.DashboardURL(srv.origin, code.Code)
	fmt.Fprintln(e.stdout, link)
	fmt.Fprintln(e.stderr, "The link works once and expires after two minutes. Any earlier link no longer works.")
	if *browser {
		opener := e.openURL
		if opener == nil {
			opener = openInBrowser
		}
		if err := opener(link); err != nil {
			return e.fail("open", fmt.Errorf("could not start the browser: %w", err))
		}
	}
	return 0
}

// openWorkspace resolves the workspace directory without creating anything.
func (e *env) openWorkspace(flagValue string) (*workspace.Workspace, error) {
	root, err := e.workspacePath(flagValue)
	if err != nil {
		return nil, err
	}
	return workspace.Open(root), nil
}

// errServerSilent marks a request the running server did not answer in time or
// at all, as opposed to one it answered with a refusal.
var errServerSilent = errors.New("the running server did not answer")

// pollDeafFor is how long a command keeps polling a server that answers
// none of its polls; the job itself keeps working in the server meanwhile.
var pollDeafFor = 2 * time.Minute

// silence counts the polls in a row the server did not answer. One missed
// answer from a busy server is asked again on the next tick; a refusal, or
// silence for pollDeafFor, ends the wait.
type silence struct{ since time.Time }

func (s *silence) tolerates(ctx context.Context, e *env, err error) bool {
	if err == nil {
		s.since = time.Time{}
		return true
	}
	if !errors.Is(err, errServerSilent) || ctx.Err() != nil {
		return false
	}
	if s.since.IsZero() {
		s.since = time.Now()
		fmt.Fprintf(e.stderr, "%v; asking again\n", err)
	}
	return time.Since(s.since) < pollDeafFor
}

// liveCall posts to the running server and decodes the success payload.
func (e *env) liveCall(ctx context.Context, srv *liveServer, method, path string, out any) error {
	return e.liveSend(ctx, srv, method, path, nil, out)
}

// liveSend is liveCall with a JSON body.
func (e *env) liveSend(ctx context.Context, srv *liveServer, method, path string, body, out any, headers ...string) error {
	return e.liveSendWithin(ctx, liveCallTimeout, srv, method, path, body, out, headers...)
}

// liveSendWithin is liveSend for a request the server answers only after work
// of its own, given wait instead of liveCallTimeout.
func (e *env) liveSendWithin(ctx context.Context, wait time.Duration, srv *liveServer, method, path string, body, out any, headers ...string) error {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			return err
		}
	}
	resp, err := srv.do(ctx, method, path, raw, headers...)
	if err != nil {
		return fmt.Errorf("%w: %w", errServerSilent, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		var f wire.Failure
		if json.NewDecoder(resp.Body).Decode(&f) == nil && f.Error.Message != "" {
			// Input the server rejects exits 2, as it does when the command
			// checks it without a server.
			if slices.Contains([]wire.ErrorCode{wire.CodeBadRequest, wire.CodeValidationFailed, wire.CodeConfigInvalid}, f.Error.Code) {
				return &usageError{"the running server refused: " + f.Error.Message}
			}
			return fmt.Errorf("the running server refused: %s", f.Error.Message)
		}
		return fmt.Errorf("the running server answered %s", resp.Status)
	}
	env := struct {
		Data json.RawMessage `json:"data"`
	}{}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return err
	}
	return json.Unmarshal(env.Data, out)
}

func openInBrowser(link string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", link)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", link)
	default:
		cmd = exec.Command("xdg-open", link)
	}
	return cmd.Start()
}
