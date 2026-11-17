package cli

import (
	"context"
	"fmt"
	"net/http"

	"applysmith/internal/api"
	"applysmith/internal/api/wire"
	"applysmith/internal/browser"
	"applysmith/internal/launch"
)

const browserUsage = `usage:
  applysmith browser status [--workspace <dir>] [--json]
  applysmith browser open [url] [--workspace <dir>]
  applysmith browser stop [--force] [--workspace <dir>]
  applysmith browser check [--workspace <dir>]

status prints the managed browser's state, its two lanes with their tabs and its newest events; it never starts the browser
open brings the apply window to the front, starting the browser first when it is not running; with a url it opens that page (http or https) in a new tab you own
stop closes the browser gracefully; it refuses while a run holds a lane unless --force is given
check loads each job site's account page in the managed browser and prints whether the profile is signed in. It only loads and reads pages. A site signed in or rate-limited less than five minutes ago is answered from that result.
status, open and stop need the app or "applysmith serve" running for the workspace
`

func (e *env) browserCmd(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(e.stderr, browserUsage)
		return 2
	}
	switch args[0] {
	case "check":
		return e.browserCheck(ctx, args[1:])
	case "status", "open", "stop":
		return e.browserLive(ctx, args[0], args[1:])
	}
	fmt.Fprint(e.stderr, browserUsage)
	return 2
}

func (e *env) browserCheck(ctx context.Context, args []string) int {
	fs := e.flags("browser check")
	ws := fs.String("workspace", "", "workspace directory")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) > 0 {
		return 2
	}
	root, err := e.workspacePath(*ws)
	if err != nil {
		return e.fail("browser check", err)
	}
	wsp, st, release, err := e.openStore(ctx, root, accessShared)
	if err != nil {
		return e.fail("browser check", err)
	}
	defer release()
	checks, err := launch.ProbeSessions(wsp, st).CheckSignIn(ctx)
	if err != nil {
		return e.fail("browser check", err)
	}
	code := 0
	for _, c := range checks {
		switch {
		case c.Err != nil:
			fmt.Fprintf(e.stdout, "%-14s not checked: %v\n", c.Site.Label, c.Err)
			code = 1
		case c.Cached:
			fmt.Fprintf(e.stdout, "%-14s %s (checked %s)\n", c.Site.Label, c.State, localTime(c.CheckedAt))
		default:
			fmt.Fprintf(e.stdout, "%-14s %s\n", c.Site.Label, c.State)
		}
		if c.State == browser.StateCheckpoint || c.State == browser.StateCaptcha {
			fmt.Fprintf(e.stdout, "%-14s next: %s\n", "", browser.NextAction(c.State))
		}
	}
	return code
}

// browserAPI is the running server's browser routes as the command uses them.
type browserAPI interface {
	status(ctx context.Context) (wire.BrowserStatus, error)
	// open brings the apply window forward; with a url it opens the page first.
	open(ctx context.Context, url string) error
	stop(ctx context.Context, force bool) (wire.BrowserStopped, error)
}

type liveBrowser struct {
	e   *env
	srv *liveServer
}

func (l liveBrowser) status(ctx context.Context) (st wire.BrowserStatus, err error) {
	err = l.e.liveCall(ctx, l.srv, http.MethodGet, "/api/browser/status", &st)
	return st, err
}

func (l liveBrowser) open(ctx context.Context, url string) error {
	if url == "" {
		var shown wire.BrowserShown
		return l.e.liveSend(ctx, l.srv, http.MethodPost, "/api/browser/apply-window/show", wire.BrowserShowRequest{}, &shown)
	}
	var opened wire.BrowserOpened
	return l.e.liveSend(ctx, l.srv, http.MethodPost, "/api/browser/apply-window/open", wire.BrowserOpenRequest{URL: url}, &opened)
}

func (l liveBrowser) stop(ctx context.Context, force bool) (out wire.BrowserStopped, err error) {
	err = l.e.liveSend(ctx, l.srv, http.MethodPost, "/api/browser/stop", wire.BrowserStopRequest{Force: force}, &out)
	return out, err
}

// browserLive runs status, open or stop against the running server.
func (e *env) browserLive(ctx context.Context, verb string, args []string) int {
	name := "browser " + verb
	fs := e.flags(name)
	wsFlag := fs.String("workspace", "", "workspace directory")
	asJSON := fs.Bool("json", false, "print JSON (status)")
	force := fs.Bool("force", false, "stop even while a run holds a lane (stop)")
	pos, err := parseMixed(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) > 1 || (verb != "open" && len(pos) > 0) || (verb != "stop" && *force) || (verb != "status" && *asJSON) {
		fmt.Fprint(e.stderr, browserUsage)
		return 2
	}
	ws, err := e.openWorkspace(*wsFlag)
	if err != nil {
		return e.fail(name, err)
	}
	held, err := api.LockHeld(ws.StateDir())
	if err != nil {
		return e.fail(name, err)
	}
	if !held {
		return e.fail(name, errNoServer)
	}
	srv, err := e.attachLive(ctx, ws)
	if err != nil {
		return e.fail(name, err)
	}
	url := ""
	if len(pos) == 1 {
		url = pos[0]
	}
	return e.runBrowser(ctx, verb, liveBrowser{e: e, srv: srv}, url, *force, *asJSON)
}

func (e *env) runBrowser(ctx context.Context, verb string, srv browserAPI, url string, force, asJSON bool) int {
	name := "browser " + verb
	switch verb {
	case "status":
		st, err := srv.status(ctx)
		if err != nil {
			return e.fail(name, err)
		}
		if asJSON {
			if e.jsonOut(st) != nil {
				return 1
			}
			return 0
		}
		e.printBrowser(st)
	case "open":
		if err := srv.open(ctx, url); err != nil {
			return e.fail(name, err)
		}
		if url == "" {
			fmt.Fprintln(e.stdout, "the apply window is in front")
		} else {
			fmt.Fprintln(e.stdout, "opened in the apply window")
		}
	case "stop":
		out, err := srv.stop(ctx, force)
		if err != nil {
			if !force {
				err = fmt.Errorf("%w (use --force to stop it anyway)", err)
			}
			return e.fail(name, err)
		}
		if out.Outcome == wire.BrowserNotRunning {
			fmt.Fprintln(e.stdout, "the browser was not running")
		} else {
			fmt.Fprintf(e.stdout, "the browser stopped: %s\n", out.Outcome)
		}
	}
	return 0
}

func (e *env) printBrowser(st wire.BrowserStatus) {
	fmt.Fprintf(e.stdout, "browser %s", st.State)
	if st.ChromeVersion != "" {
		fmt.Fprintf(e.stdout, " (%s)", st.ChromeVersion)
	}
	fmt.Fprintln(e.stdout)
	if st.Error != "" {
		fmt.Fprintf(e.stdout, "last failure: %s\n", st.Error)
	}
	for _, l := range []struct {
		name string
		lane wire.BrowserLane
	}{{"apply", st.Apply}, {"scan", st.Scan}} {
		window := "no window"
		if l.lane.Window {
			window = "window"
		}
		fmt.Fprintf(e.stdout, "%s lane: %s, %d of %d tabs\n", l.name, window, len(l.lane.Tabs), l.lane.TabCap)
		for _, t := range l.lane.Tabs {
			flags := ""
			if t.Active {
				flags += " active"
			}
			if t.Kept {
				flags += " kept"
			}
			if t.Owner != "" {
				flags += " run " + t.Owner
			}
			fmt.Fprintf(e.stdout, "  %s %s%s\n", t.ID, t.URL, flags)
		}
	}
	for i, ev := range st.Events {
		if i == 5 {
			break
		}
		fmt.Fprintf(e.stdout, "event %s %s %s\n", localTime(ev.At.Time), ev.Kind, ev.Detail)
	}
}
