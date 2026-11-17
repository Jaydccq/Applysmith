package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"golang.org/x/term"

	"applysmith/internal/api"
	"applysmith/internal/launch"
	"applysmith/internal/onboarding"
	"applysmith/internal/workspace"
)

func stdinIsTerminal() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

func (e *env) in() io.Reader {
	if e.stdin == nil {
		return os.Stdin
	}
	return e.stdin
}

func (e *env) isTerminal() bool { return e.terminal != nil && e.terminal() }

// onboardingEnv builds the environment the setup and doctor code inspects.
func (e *env) onboardingEnv(wsFlag string) *onboarding.Env {
	oe := &onboarding.Env{Backend: e.backend, Getenv: e.getenv, HomeDir: e.homeDir, ConfigDir: e.configDir, LLMOptions: e.llmOptions}
	if root, err := e.newWorkspacePath(wsFlag); err != nil {
		oe.ResolveErr = err
	} else {
		oe.Workspace = workspace.Open(root)
		oe.BrowserCheck = launch.BrowserCheck(oe.Workspace)
	}
	if e.hook != nil {
		e.hook(oe)
	}
	return oe
}

// refuseMissing refuses a workspace folder that does not exist, as
// workspacePath does, for every command built on the setup environment but
// setup's own, which may create the workspace.
func (e *env) refuseMissing(cmd, wsFlag string) (int, bool) {
	if cmd == "setup" || strings.HasPrefix(cmd, "setup ") {
		return 0, false
	}
	if _, err := e.workspacePath(wsFlag); err != nil {
		return e.fail(cmd, err), true
	}
	return 0, false
}

// withRead runs fn with the database open read-only.
func (e *env) withRead(ctx context.Context, cmd, wsFlag string, fn func(*onboarding.Env) int) int {
	if code, refused := e.refuseMissing(cmd, wsFlag); refused {
		return code
	}
	oe := e.onboardingEnv(wsFlag)
	closeFn, err := oe.Open(ctx, false)
	if err != nil {
		return e.report(cmd, &onboarding.EnvError{Message: "the workspace database cannot be opened", Err: err})
	}
	defer closeFn()
	return fn(oe)
}

// withWrite runs fn under the workspace lock with the database open for
// writing. A running server holds the workspace, so the command reports that.
// A workspace that has not been created yet gets neither lock nor database, so
// nothing is written before the workspace step has chosen where it lives.
func (e *env) withWrite(ctx context.Context, cmd, wsFlag string, fn func(*onboarding.Env) int) int {
	return e.withWriteAfterWorkspace(ctx, cmd, wsFlag, func(oe *onboarding.Env, _ func(context.Context) error) int { return fn(oe) })
}

// withWriteAfterWorkspace is withWrite for the command that creates the
// workspace itself: fn gets prepare, which takes the lock and opens the
// database once the workspace exists (and refreshes them when already held).
func (e *env) withWriteAfterWorkspace(ctx context.Context, cmd, wsFlag string, fn func(*onboarding.Env, func(context.Context) error) int) int {
	if code, refused := e.refuseMissing(cmd, wsFlag); refused {
		return code
	}
	oe := e.onboardingEnv(wsFlag)
	var release func()
	defer func() {
		if release != nil {
			release()
		}
	}()
	prepare := func(ctx context.Context) error {
		if oe.Workspace == nil {
			return nil
		}
		if release != nil {
			_, err := oe.Open(ctx, true)
			return err
		}
		r, err := e.lockAndOpen(ctx, oe)
		if err != nil {
			return err
		}
		release = r
		return nil
	}
	if oe.Workspace != nil {
		if _, err := os.Stat(oe.Workspace.Path(workspace.FileProfile)); err == nil {
			if err := prepare(ctx); err != nil {
				return e.report(cmd, err)
			}
		}
	}
	return fn(oe, prepare)
}

// lockAndOpen takes the workspace lock and opens the database for writing.
func (e *env) lockAndOpen(ctx context.Context, oe *onboarding.Env) (func(), error) {
	if _, err := oe.Workspace.ID(); err != nil {
		return nil, &onboarding.EnvError{Message: "the workspace cannot be prepared", Err: err}
	}
	lock, err := api.AcquireLock(oe.Workspace.StateDir())
	if api.IsStartupCode(err, api.CodeServerAlreadyRunning) || api.IsStartupCode(err, api.CodeWorkspaceLocked) {
		msg := "a server holds this workspace; stop it, or use the dashboard"
		if addr := onboarding.WizardAddress(oe.Workspace); addr != "" {
			msg = "a server holds this workspace; continue in the setup wizard at " + addr + " (`applysmith open` prints a sign-in link), or stop the server"
		}
		return nil, &onboarding.EnvError{Message: msg, Err: err}
	}
	if err != nil {
		return nil, &onboarding.EnvError{Message: "the workspace lock cannot be taken", Err: err}
	}
	closeFn, err := oe.Open(ctx, true)
	if err != nil {
		lock.Release()
		return nil, &onboarding.EnvError{Message: "the workspace database cannot be opened", Err: err}
	}
	return func() { closeFn(); lock.Release() }, nil
}

// report prints an error and returns the exit code the setup commands use:
// 2 rejected input, 3 a person must act, 4 the environment failed, 1 other.
func (e *env) report(cmd string, err error) int {
	var fe onboarding.FieldErrors
	var rej *onboarding.RejectedError
	var stale *onboarding.StaleError
	var human *onboarding.HumanError
	var stopped *onboarding.StoppedError
	var envErr *onboarding.EnvError
	switch {
	case errors.As(err, &fe):
		keys := make([]string, 0, len(fe))
		for k := range fe {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintf(e.stderr, "applysmith %s: input rejected\n", cmd)
		for _, k := range keys {
			fmt.Fprintf(e.stderr, "  %s: %s\n", k, fe[k])
		}
		return 2
	case errors.As(err, &rej), errors.As(err, &stale), errors.Is(err, onboarding.ErrScriptExhausted):
		fmt.Fprintf(e.stderr, "applysmith %s: %v\n", cmd, err)
		return 2
	case errors.As(err, &human):
		fmt.Fprintf(e.stderr, "applysmith %s: a person must act: %v\n", cmd, human)
		return 3
	case errors.As(err, &stopped):
		fmt.Fprintf(e.stderr, "applysmith %s: %v\n  fix it and run `applysmith setup` again\n", cmd, stopped)
		return 3
	case errors.As(err, &envErr):
		fmt.Fprintf(e.stderr, "applysmith %s: %v\n", cmd, err)
		return 4
	}
	fmt.Fprintf(e.stderr, "applysmith %s: %v\n", cmd, err)
	return 1
}

// prompter reads answers from a script, or from the terminal with secrets
// hidden.
func (e *env) prompter(answers string) (onboarding.Prompter, io.Closer, error) {
	switch {
	case answers == "-":
		return onboarding.NewLinePrompter(e.in(), e.stdout), nopCloser{}, nil
	case answers != "":
		f, err := os.Open(answers)
		if err != nil {
			return nil, nil, err
		}
		return onboarding.NewLinePrompter(f, e.stdout), f, nil
	}
	p := onboarding.NewLinePrompter(bufio.NewReader(e.in()), e.stdout)
	if f, ok := e.in().(*os.File); ok {
		p.Hidden = func() (string, error) {
			b, err := term.ReadPassword(int(f.Fd()))
			return strings.TrimSpace(string(b)), err
		}
	}
	return p, nopCloser{}, nil
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }
