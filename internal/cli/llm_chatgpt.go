package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"applysmith/internal/api"
	"applysmith/internal/api/wire"
)

const chatgptNeedsServer = "the running server owns the ChatGPT sign-in, because its refresh token rotates on every use; start the app or `applysmith serve`, then run the command again"

// chatgptServer finds the server that holds the workspace. The sign-in belongs
// to it, so a command line with no server beside it cannot start or end one.
// Without one it returns nil and the exit code of the failure it printed.
func (e *env) chatgptServer(ctx context.Context, cmd, wsFlag string) (*liveServer, int) {
	ws, err := e.openWorkspace(wsFlag)
	if err != nil {
		return nil, e.fail(cmd, err)
	}
	held, err := api.LockHeld(ws.StateDir())
	if err != nil {
		return nil, e.fail(cmd, err)
	}
	if !held {
		return nil, e.fail(cmd, errors.New(chatgptNeedsServer))
	}
	srv, err := e.attachLive(ctx, ws)
	if err != nil {
		return nil, e.fail(cmd, err)
	}
	return srv, 0
}

// llmSignin starts Sign in with ChatGPT in the server, prints the address to
// open, and waits for the browser to come back to the server's loopback address.
func (e *env) llmSignin(ctx context.Context, args []string) int {
	fs := e.flags("llm signin")
	wsFlag := fs.String("workspace", "", "workspace directory")
	open := fs.Bool("open", false, "also open the address in the default browser")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 1 || pos[0] != "chatgpt" {
		fmt.Fprint(e.stderr, llmUsage)
		return 2
	}
	srv, code := e.chatgptServer(ctx, "llm signin", *wsFlag)
	if srv == nil {
		return code
	}
	var started wire.ChatGPTSignInStarted
	if err := e.liveCall(ctx, srv, http.MethodPost, "/api/llm/chatgpt/signin", &started); err != nil {
		return e.fail("llm signin", err)
	}
	fmt.Fprintln(e.stdout, started.URL)
	fmt.Fprintln(e.stderr, "Open this address in your browser and sign in to ChatGPT; allow plan usage. Waiting for the browser to come back...")
	if *open {
		opener := e.openURL
		if opener == nil {
			opener = openInBrowser
		}
		if err := opener(started.URL); err != nil {
			fmt.Fprintf(e.stderr, "could not start the browser: %v\n", err)
		}
	}
	var quiet silence
	for {
		select {
		case <-ctx.Done():
			return e.fail("llm signin", errors.New("interrupted; the sign-in keeps waiting in the server for ten minutes"))
		case <-time.After(evaluatePoll):
		}
		var st wire.ChatGPTStatus
		err := e.liveCall(ctx, srv, http.MethodGet, "/api/llm/chatgpt", &st)
		if !quiet.tolerates(ctx, e, err) {
			return e.fail("llm signin", err)
		}
		if err != nil || st.Pending {
			continue
		}
		if st.SignedIn && st.PlanUsage && st.Message == "" {
			fmt.Fprintf(e.stdout, "signed in to ChatGPT as %s; run `applysmith llm configure --preset chatgpt` to route tasks to it\n", st.Account)
			return 0
		}
		return e.fail("llm signin", errors.New(st.Message))
	}
}

// llmSignout ends the sign-in in the server.
func (e *env) llmSignout(ctx context.Context, args []string) int {
	fs := e.flags("llm signout")
	wsFlag := fs.String("workspace", "", "workspace directory")
	forget := fs.Bool("forget", false, "also forget the account, so another account can sign in")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 1 || pos[0] != "chatgpt" {
		fmt.Fprint(e.stderr, llmUsage)
		return 2
	}
	srv, code := e.chatgptServer(ctx, "llm signout", *wsFlag)
	if srv == nil {
		return code
	}
	var res wire.ChatGPTSignOutResult
	if err := e.liveSend(ctx, srv, http.MethodPost, "/api/llm/chatgpt/signout", wire.ChatGPTSignOutRequest{Forget: *forget}, &res); err != nil {
		return e.fail("llm signout", err)
	}
	fmt.Fprintln(e.stdout, "signed out of ChatGPT; the tokens are removed from this computer")
	if !res.Revoked {
		fmt.Fprintln(e.stderr, "OpenAI did not confirm the revocation; disconnect Applysmith in ChatGPT settings to be sure.")
	}
	return 0
}
